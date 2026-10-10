package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AliJabbar034/vrok/internal/inbox"
	"github.com/AliJabbar034/vrok/internal/sharing"
	"github.com/AliJabbar034/vrok/web/viewer"
)

const (
	// uploadPath is the API under a receive share. A receive share serves no
	// files, so nothing a visitor could name collides with it.
	uploadPath = "_upload"

	// uploadHeader must accompany every request that changes an upload. A
	// custom header makes a cross-site request need a CORS preflight, which
	// vrok never answers, so another site cannot post files into the inbox.
	uploadHeader = "X-Vrok-Upload"

	// maxChunkBytes bounds one chunk. The page sends 8 MiB, well under the
	// 100 MB a Cloudflare tunnel accepts in one request.
	maxChunkBytes = 16 << 20
	// maxBeginBytes bounds the JSON that starts an upload.
	maxBeginBytes = 4 << 10

	// uploadIdle is how long an upload may go without a chunk before it
	// stops counting as an active transfer, so the owner's progress line
	// and keep-awake do not wait on a sender who walked away.
	uploadIdle = 30 * time.Second
	// uploadAbandon is how long a silent upload is kept for resuming before
	// its part file is discarded and its slot in --max-files is returned.
	uploadAbandon = 15 * time.Minute
)

// receiveCSP locks the upload page down to vrok's own script and styles.
const receiveCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// errInboxFull is what a visitor is told when a receive share has accepted
// every file it was allowed to.
var errInboxFull = errors.New("server: receive share is full")

// receivers holds the open inbox of every receive share, keyed by share id.
type receivers struct {
	clock      sharing.Clock
	onReceived func(sharing.Spec, inbox.Received)
	// onOffer asks the owner about a batch of files. When nil, every offer
	// is accepted as it arrives.
	onOffer func(sharing.Spec, *Offer)

	mu      sync.Mutex
	byShare map[string]*receiver
}

func newReceivers(clock sharing.Clock, onReceived func(sharing.Spec, inbox.Received), onOffer func(sharing.Spec, *Offer)) *receivers {
	return &receivers{clock: clock, onReceived: onReceived, onOffer: onOffer, byShare: make(map[string]*receiver)}
}

// receiver is one receive share's inbox and the uploads running into it.
type receiver struct {
	inbox *inbox.Inbox

	mu     sync.Mutex
	active map[string]*activeUpload
	offers map[string]*Offer
}

// activeUpload tracks the live-progress side of one upload. The bytes
// themselves are the inbox's business.
type activeUpload struct {
	share    *sharing.Share
	offer    *Offer
	offerID  string
	size     int64
	transfer *sharing.Transfer // nil while no chunk is arriving
	inFlight int
	idle     *time.Timer
	abandon  *time.Timer
}

func (rs *receivers) get(spec sharing.Spec) (*receiver, error) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rc, ok := rs.byShare[spec.ID]; ok {
		return rc, nil
	}
	box, err := inbox.Open(spec.Root)
	if err != nil {
		return nil, err
	}
	rc := &receiver{inbox: box, active: make(map[string]*activeUpload), offers: make(map[string]*Offer)}
	rs.byShare[spec.ID] = rc
	return rc, nil
}

// continues reports whether rel addresses an upload already under way, which
// keeps working after the share has accepted its last new file.
func (rs *receivers) continues(shareID, rel string) bool {
	id, ok := strings.CutPrefix(rel, uploadPath+"/")
	if !ok || id == "" {
		return false
	}
	rs.mu.Lock()
	rc := rs.byShare[shareID]
	rs.mu.Unlock()
	if rc == nil {
		return false
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	_, ok = rc.active[id]
	return ok
}

// forget closes a share's inbox, discarding its unfinished uploads.
func (rs *receivers) forget(shareID string) {
	rs.mu.Lock()
	rc := rs.byShare[shareID]
	delete(rs.byShare, shareID)
	rs.mu.Unlock()
	if rc != nil {
		rc.close()
	}
}

func (rs *receivers) closeAll() {
	rs.mu.Lock()
	all := rs.byShare
	rs.byShare = make(map[string]*receiver)
	rs.mu.Unlock()
	for _, rc := range all {
		rc.close()
	}
}

func (rc *receiver) close() {
	rc.declineAll()
	rc.mu.Lock()
	for id, u := range rc.active {
		u.stop(time.Now())
		delete(rc.active, id)
	}
	rc.mu.Unlock()
	rc.inbox.Close()
}

// stop ends the upload's live transfer and its timers. The caller holds the
// receiver's lock.
func (u *activeUpload) stop(now time.Time) {
	if u.transfer != nil {
		u.transfer.End(now)
		u.transfer = nil
	}
	u.idle.Stop()
	u.abandon.Stop()
}

// ReceiveHandler serves a receive share: the upload page and the chunked
// upload API behind it.
type ReceiveHandler struct {
	receivers *receivers
	pages     *pages
}

// ServeShare implements ShareHandler.
func (h ReceiveHandler) ServeShare(w http.ResponseWriter, r *http.Request, sr *shareRequest) {
	rc, err := h.receivers.get(sr.Spec)
	if err != nil {
		h.pages.serverError(w, r, err)
		return
	}

	if sr.Rel == "" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
			return
		}
		h.page(w, r, sr)
		return
	}

	if r.Method != http.MethodGet && r.Header.Get(uploadHeader) != "1" {
		uploadError(w, http.StatusForbidden, "This request did not come from the upload page.")
		return
	}
	if rest, ok := strings.CutPrefix(sr.Rel, offerPath); ok {
		id := strings.TrimPrefix(rest, "/")
		switch {
		case rest == "" && r.Method == http.MethodPost:
			h.offer(w, r, sr, rc)
		case rest != "" && id != "" && !strings.Contains(id, "/") && r.Method == http.MethodGet:
			h.answer(w, r, rc, id)
		default:
			h.pages.gone(w, r, nil)
		}
		return
	}
	rest, ok := strings.CutPrefix(sr.Rel, uploadPath)
	if !ok {
		h.pages.gone(w, r, nil)
		return
	}

	id := strings.TrimPrefix(rest, "/")
	switch {
	case rest == "" && r.Method == http.MethodPost:
		h.begin(w, r, sr, rc)
	case rest == "" || strings.Contains(id, "/"):
		h.pages.gone(w, r, nil)
	case r.Method == http.MethodGet:
		h.status(w, rc, id)
	case r.Method == http.MethodPut:
		h.chunk(w, r, rc, id)
	case r.Method == http.MethodPost:
		h.finish(w, sr, rc, id)
	case r.Method == http.MethodDelete:
		h.abort(w, rc, id)
	default:
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
	}
}

func (h ReceiveHandler) page(w http.ResponseWriter, r *http.Request, sr *shareRequest) {
	noStore(w)
	w.Header().Set("Content-Security-Policy", receiveCSP)
	meta := h.pages.meta(sr, "Send files · vrok")
	// The header pill counts downloads; for an inbox the page says how many
	// files are left in words instead.
	meta.Downloads = ""
	data := viewer.UploadPage{Meta: meta, UploadURL: sr.Links.Page(uploadPath), OfferURL: sr.Links.Page(offerPath)}
	if remaining, limited := sharing.RemainingDownloads(sr.Snap); limited {
		data.FilesLeft = remaining
	}
	if err := h.pages.render.Upload(w, http.StatusOK, data); err != nil {
		h.pages.serverError(w, r, err)
	}
}

// offer asks the owner whether a batch of files may be sent. The batch is
// checked against the file limit and the free disk first, so the owner is
// never asked about files that could not be taken anyway.
func (h ReceiveHandler) offer(w http.ResponseWriter, r *http.Request, sr *shareRequest, rc *receiver) {
	var req struct {
		Files []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"files"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOfferBytes)).Decode(&req); err != nil ||
		len(req.Files) == 0 || len(req.Files) > maxOfferFiles {
		uploadError(w, http.StatusBadRequest, fmt.Sprintf("Choose between 1 and %d files at a time.", maxOfferFiles))
		return
	}
	names := make([]string, len(req.Files))
	sizes := make([]int64, len(req.Files))
	var total int64
	for i, f := range req.Files {
		if f.Size < 0 || f.Size > math.MaxInt64-total {
			uploadError(w, http.StatusBadRequest, "Those files could not be read.")
			return
		}
		names[i], sizes[i] = f.Name, f.Size
		total += f.Size
	}

	if remaining, limited := sharing.RemainingDownloads(sr.Snap); limited && len(req.Files) > remaining {
		if remaining == 0 {
			uploadError(w, http.StatusGone, "This link is not accepting more files. Ask the person who sent it for a new one.")
			return
		}
		uploadError(w, http.StatusConflict, fmt.Sprintf("This link accepts only %d more %s. Choose fewer.", remaining, plural(remaining, "file", "files")))
		return
	}
	if err := rc.inbox.Fits(total); err != nil {
		uploadError(w, http.StatusInsufficientStorage, "The receiving computer does not have enough free space for these files.")
		return
	}

	o := newOffer(names, sizes)
	id, ok := rc.addOffer(o)
	if !ok {
		uploadError(w, http.StatusServiceUnavailable, "The receiver is answering another request. Try again in a moment.")
		return
	}
	if h.receivers.onOffer == nil {
		o.Accept()
	} else {
		h.receivers.onOffer(sr.Spec, o)
	}
	writeUploadJSON(w, http.StatusCreated, map[string]any{"id": id, "state": o.State()})
}

// answer tells the page what the owner decided, holding the request open
// for a while so the page hears the moment it happens.
func (h ReceiveHandler) answer(w http.ResponseWriter, r *http.Request, rc *receiver, id string) {
	o := rc.offer(id)
	if o == nil {
		uploadError(w, http.StatusNotFound, "This request is no longer open. Choose the files again.")
		return
	}
	wait := time.NewTimer(offerPoll)
	defer wait.Stop()
	select {
	case <-o.Decided():
	case <-wait.C:
	case <-r.Context().Done():
		return
	}
	writeUploadJSON(w, http.StatusOK, map[string]any{"state": o.State()})
}

// begin starts an upload of a file the owner accepted. It claims a slot
// from --max-files before anything is written, so the limit holds even when
// several files start at once.
func (h ReceiveHandler) begin(w http.ResponseWriter, r *http.Request, sr *shareRequest, rc *receiver) {
	var req struct {
		Offer string `json:"offer"`
		Name  string `json:"name"`
		Size  int64  `json:"size"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBeginBytes)).Decode(&req); err != nil || req.Size < 0 {
		uploadError(w, http.StatusBadRequest, "That upload could not be started.")
		return
	}
	o := rc.offer(req.Offer)
	if o == nil || !o.take(req.Name, req.Size) {
		uploadError(w, http.StatusForbidden, "The receiver has not accepted this file.")
		return
	}

	if _, err := sr.Share.ClaimDownload(h.receivers.clock.Now()); err != nil {
		o.giveBack(req.Name, req.Size)
		uploadError(w, http.StatusGone, "This link is not accepting more files. Ask the person who sent it for a new one.")
		return
	}
	id, err := rc.inbox.Begin(req.Name, req.Size)
	if err != nil {
		o.giveBack(req.Name, req.Size)
		sr.Share.ReleaseDownload()
		switch {
		case errors.Is(err, inbox.ErrNoSpace):
			uploadError(w, http.StatusInsufficientStorage, "The receiving computer does not have enough free space for this file.")
		case errors.Is(err, inbox.ErrBusy):
			uploadError(w, http.StatusServiceUnavailable, "Too many uploads at once. Try again in a moment.")
		default:
			h.pages.logger.Error("upload could not start", slog.String("error", err.Error()))
			uploadError(w, http.StatusInternalServerError, "The receiving computer could not save this file.")
		}
		return
	}

	u := &activeUpload{share: sr.Share, offer: o, offerID: req.Offer, size: req.Size}
	u.idle = time.AfterFunc(uploadIdle, func() { rc.idle(id) })
	u.abandon = time.AfterFunc(uploadAbandon, func() { rc.drop(id) })
	rc.mu.Lock()
	rc.active[id] = u
	rc.mu.Unlock()

	writeUploadJSON(w, http.StatusCreated, map[string]any{"id": id, "offset": 0})
}

func (h ReceiveHandler) status(w http.ResponseWriter, rc *receiver, id string) {
	written, size, err := rc.inbox.Status(id)
	if err != nil {
		uploadError(w, http.StatusNotFound, "This upload is no longer open. Choose the file again.")
		return
	}
	writeUploadJSON(w, http.StatusOK, map[string]any{"offset": written, "size": size})
}

func (h ReceiveHandler) chunk(w http.ResponseWriter, r *http.Request, rc *receiver, id string) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		uploadError(w, http.StatusBadRequest, "That chunk could not be read.")
		return
	}
	transfer, ok := rc.startChunk(id)
	if !ok {
		uploadError(w, http.StatusNotFound, "This upload is no longer open. Choose the file again.")
		return
	}
	defer rc.endChunk(id)

	written, err := rc.inbox.Write(id, offset, http.MaxBytesReader(w, r.Body, maxChunkBytes), transfer.Add)
	var maxBytes *http.MaxBytesError
	switch {
	case err == nil:
		writeUploadJSON(w, http.StatusOK, map[string]any{"offset": written})
	case errors.Is(err, inbox.ErrOffset):
		writeUploadJSON(w, http.StatusConflict, map[string]any{"offset": written})
	case errors.Is(err, inbox.ErrTooLarge), errors.As(err, &maxBytes):
		uploadError(w, http.StatusRequestEntityTooLarge, "The file sent more data than its size.")
	case errors.Is(err, inbox.ErrUnknownUpload):
		uploadError(w, http.StatusNotFound, "This upload is no longer open. Choose the file again.")
	case errors.Is(err, inbox.ErrBusy):
		writeUploadJSON(w, http.StatusConflict, map[string]any{"offset": written})
	default:
		// Usually the sender's connection dropped mid-chunk. What did
		// arrive is kept, and the page resumes from the reported offset.
		writeUploadJSON(w, http.StatusServiceUnavailable, map[string]any{"offset": written})
	}
}

func (h ReceiveHandler) finish(w http.ResponseWriter, sr *shareRequest, rc *receiver, id string) {
	got, err := rc.inbox.Finish(id)
	switch {
	case errors.Is(err, inbox.ErrIncomplete):
		written, _, _ := rc.inbox.Status(id)
		writeUploadJSON(w, http.StatusConflict, map[string]any{"offset": written})
		return
	case errors.Is(err, inbox.ErrUnknownUpload):
		uploadError(w, http.StatusNotFound, "This upload is no longer open. Choose the file again.")
		return
	case err != nil:
		rc.forgetUpload(id, false)
		sr.Share.ReleaseDownload()
		h.pages.logger.Error("upload could not be saved", slog.String("error", err.Error()))
		uploadError(w, http.StatusInternalServerError, "The receiving computer could not save this file.")
		return
	}
	rc.forgetUpload(id, true)
	if h.receivers.onReceived != nil {
		h.receivers.onReceived(sr.Spec, got)
	}
	// The final name is not returned: it would tell the sender which names
	// the owner's folder already holds.
	writeUploadJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h ReceiveHandler) abort(w http.ResponseWriter, rc *receiver, id string) {
	rc.drop(id)
	w.WriteHeader(http.StatusNoContent)
}

// startChunk marks a chunk as arriving: the upload counts as an active
// transfer, resumed at the bytes already in, and its idle timers pause.
func (rc *receiver) startChunk(id string) (*sharing.Transfer, bool) {
	written, _, err := rc.inbox.Status(id)
	if err != nil {
		return nil, false
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	u, ok := rc.active[id]
	if !ok {
		return nil, false
	}
	if u.transfer == nil {
		u.transfer = u.share.BeginTransfer()
		u.transfer.SetTotal(u.size)
		u.transfer.Resume(written)
	}
	u.inFlight++
	u.idle.Stop()
	u.abandon.Stop()
	return u.transfer, true
}

// endChunk restarts the idle timers once no chunk is arriving.
func (rc *receiver) endChunk(id string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	u, ok := rc.active[id]
	if !ok {
		return
	}
	u.inFlight--
	if u.inFlight == 0 {
		u.idle.Reset(uploadIdle)
		u.abandon.Reset(uploadAbandon)
	}
}

// idle ends the live transfer of an upload that has gone quiet. It stays
// open for resuming.
func (rc *receiver) idle(id string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if u, ok := rc.active[id]; ok && u.inFlight == 0 && u.transfer != nil {
		u.transfer.End(time.Now())
		u.transfer = nil
	}
}

// drop discards an upload: its part file, its live transfer, and the slot it
// claimed from --max-files.
func (rc *receiver) drop(id string) {
	rc.mu.Lock()
	u, ok := rc.active[id]
	if ok {
		u.stop(time.Now())
		delete(rc.active, id)
	}
	rc.mu.Unlock()
	if ok {
		rc.inbox.Abort(id)
		u.share.ReleaseDownload()
	}
}

// forgetUpload stops tracking an upload that has ended. An offer whose last
// file has arrived is forgotten too: nothing more can be sent under it.
func (rc *receiver) forgetUpload(id string, arrived bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	u, ok := rc.active[id]
	if !ok {
		return
	}
	u.stop(time.Now())
	delete(rc.active, id)
	if arrived && u.offer.received() {
		delete(rc.offers, u.offerID)
	}
}

func writeUploadJSON(w http.ResponseWriter, status int, payload any) {
	noStore(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// uploadError answers the page with a message it shows the sender as is.
func uploadError(w http.ResponseWriter, status int, message string) {
	writeUploadJSON(w, status, map[string]string{"error": message})
}
