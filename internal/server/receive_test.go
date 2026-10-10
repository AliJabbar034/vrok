package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/AliJabbar034/vrok/internal/inbox"
	"github.com/AliJabbar034/vrok/internal/server"
	"github.com/AliJabbar034/vrok/internal/sharing"
)

// receiveFixture is a running server with one receive share, and a record
// of every file it reported as received.
type receiveFixture struct {
	*fixture
	dir string

	mu       sync.Mutex
	received []inbox.Received
}

// newReceiveFixture accepts every offer unless onOffer is given, which
// then plays the owner.
func newReceiveFixture(t *testing.T, spec sharing.Spec, onOffer ...func(*server.Offer)) *receiveFixture {
	t.Helper()
	rf := &receiveFixture{dir: t.TempDir()}
	spec.Kind = sharing.KindReceive
	spec.Name = "inbox"
	spec.Root = rf.dir
	rf.fixture = newFixture(t, spec, func(o *server.Options) {
		o.OnReceived = func(_ sharing.Spec, got inbox.Received) {
			rf.mu.Lock()
			rf.received = append(rf.received, got)
			rf.mu.Unlock()
		}
		if len(onOffer) > 0 {
			o.OnOffer = func(_ sharing.Spec, offer *server.Offer) { onOffer[0](offer) }
		}
	})
	return rf
}

// offer asks to send files, given as name and size pairs.
func (rf *receiveFixture) offer(files ...any) (int, map[string]any) {
	rf.t.Helper()
	var list []map[string]any
	for i := 0; i < len(files); i += 2 {
		list = append(list, map[string]any{"name": files[i], "size": files[i+1]})
	}
	body, _ := json.Marshal(map[string]any{"files": list})
	req, err := http.NewRequest(http.MethodPost, rf.url("_offer"), bytes.NewReader(body))
	if err != nil {
		rf.t.Fatal(err)
	}
	req.Header.Set("X-Vrok-Upload", "1")
	return rf.do(req)
}

// answer asks what the owner decided about an offer.
func (rf *receiveFixture) answer(id string) (int, map[string]any) {
	rf.t.Helper()
	req, err := http.NewRequest(http.MethodGet, rf.url("_offer/"+id), nil)
	if err != nil {
		rf.t.Fatal(err)
	}
	return rf.do(req)
}

func (rf *receiveFixture) do(req *http.Request) (int, map[string]any) {
	rf.t.Helper()
	resp, err := rf.client.Do(req)
	if err != nil {
		rf.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	var data map[string]any
	json.NewDecoder(resp.Body).Decode(&data)
	return resp.StatusCode, data
}

// startWith begins an upload under an offer.
func (rf *receiveFixture) startWith(offer, name string, size int) (int, map[string]any) {
	rf.t.Helper()
	body, _ := json.Marshal(map[string]any{"offer": offer, "name": name, "size": size})
	return rf.api(http.MethodPost, "", body)
}

// api sends one request to the upload API, as upload.js does, and returns
// the status with the decoded JSON body (nil when the body is not JSON).
func (rf *receiveFixture) api(method, rel string, body []byte) (int, map[string]any) {
	rf.t.Helper()
	status, data, _ := rf.apiRaw(method, rel, body, true)
	return status, data
}

func (rf *receiveFixture) apiRaw(method, rel string, body []byte, header bool) (int, map[string]any, string) {
	rf.t.Helper()
	req, err := http.NewRequest(method, rf.url("_upload"+rel), bytes.NewReader(body))
	if err != nil {
		rf.t.Fatal(err)
	}
	if header {
		req.Header.Set("X-Vrok-Upload", "1")
	}
	resp, err := rf.client.Do(req)
	if err != nil {
		rf.t.Fatalf("%s %s: %v", method, rel, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		data = nil
	}
	return resp.StatusCode, data, string(raw)
}

// begin offers one file, which the fixture accepts, starts its upload and
// returns the upload's ID.
func (rf *receiveFixture) begin(name string, size int) string {
	rf.t.Helper()
	status, data := rf.offer(name, size)
	if status != http.StatusCreated || data["state"] != "accepted" {
		rf.t.Fatalf("offer %s: status %d, body %v", name, status, data)
	}
	status, data = rf.startWith(data["id"].(string), name, size)
	if status != http.StatusCreated {
		rf.t.Fatalf("begin %s: status %d, body %v", name, status, data)
	}
	return data["id"].(string)
}

func (rf *receiveFixture) chunk(id string, offset int, data []byte) (int, map[string]any) {
	rf.t.Helper()
	return rf.api(http.MethodPut, "/"+id+"?offset="+strconv.Itoa(offset), data)
}

func (rf *receiveFixture) finish(id string) (int, map[string]any) {
	rf.t.Helper()
	return rf.api(http.MethodPost, "/"+id, nil)
}

// send uploads a whole file in one chunk.
func (rf *receiveFixture) send(name string, content []byte) {
	rf.t.Helper()
	id := rf.begin(name, len(content))
	if status, data := rf.chunk(id, 0, content); status != http.StatusOK {
		rf.t.Fatalf("chunk %s: status %d, body %v", name, status, data)
	}
	if status, data := rf.finish(id); status != http.StatusOK {
		rf.t.Fatalf("finish %s: status %d, body %v", name, status, data)
	}
}

func (rf *receiveFixture) read(name string) string {
	rf.t.Helper()
	data, err := os.ReadFile(filepath.Join(rf.dir, name))
	if err != nil {
		rf.t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestReceivePageIsLockedDown(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{MaxDownloads: 3})

	resp := rf.get("")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := rf.body(resp)
	if !strings.Contains(body, "upload.js") || !strings.Contains(body, "3 more files") {
		t.Error("the upload page does not load its script or show the file limit")
	}
	if strings.Contains(body, rf.dir) {
		t.Error("the upload page reveals the folder on the receiving machine")
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("Content-Security-Policy = %q, want scripts and requests limited to the share", csp)
	}
}

func TestReceiveChunkedUploadResumes(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{})
	content := []byte(strings.Repeat("vrok", 1000))
	id := rf.begin("../../report.pdf", len(content))

	if status, data := rf.chunk(id, 0, content[:1500]); status != http.StatusOK || data["offset"] != 1500.0 {
		t.Fatalf("first chunk: status %d, body %v", status, data)
	}
	// A retried chunk the server already has is refused with where to resume.
	if status, data := rf.chunk(id, 0, content[:1500]); status != http.StatusConflict || data["offset"] != 1500.0 {
		t.Fatalf("repeated chunk: status %d, body %v; want 409 with offset 1500", status, data)
	}
	if status, data := rf.api(http.MethodGet, "/"+id, nil); status != http.StatusOK || data["offset"] != 1500.0 {
		t.Fatalf("status: %d, body %v", status, data)
	}
	if status, _ := rf.finish(id); status != http.StatusConflict {
		t.Fatalf("finishing early: status %d, want 409", status)
	}
	if status, _ := rf.chunk(id, 1500, content[1500:]); status != http.StatusOK {
		t.Fatalf("second chunk: status %d", status)
	}
	status, data := rf.finish(id)
	if status != http.StatusOK {
		t.Fatalf("finish: status %d, body %v", status, data)
	}
	if _, leaked := data["name"]; leaked {
		t.Error("the sender is told the name the file was saved under")
	}

	if got := rf.read("report.pdf"); got != string(content) {
		t.Errorf("saved %d bytes, want the %d sent", len(got), len(content))
	}
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if len(rf.received) != 1 || rf.received[0].Name != "report.pdf" || rf.received[0].Size != int64(len(content)) {
		t.Errorf("OnReceived saw %+v, want report.pdf of %d bytes", rf.received, len(content))
	}
}

func TestReceiveNeverOverwrites(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{})
	rf.send("photo.jpg", []byte("first"))
	rf.send("photo.jpg", []byte("second"))

	if rf.read("photo.jpg") != "first" || rf.read("photo (1).jpg") != "second" {
		t.Error("a second file with the same name replaced the first")
	}
}

func TestReceiveRequiresTheUploadHeader(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{})
	body := []byte(`{"name":"a.txt","size":1}`)
	if status, _, _ := rf.apiRaw(http.MethodPost, "", body, false); status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: a form on another site must not be able to upload", status)
	}
	entries, _ := os.ReadDir(rf.dir)
	if len(entries) != 0 {
		t.Errorf("the refused request left %d files behind", len(entries))
	}
}

func TestReceiveRejectsOtherMethods(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{})
	if status, _, _ := rf.apiRaw(http.MethodPatch, "", nil, true); status != http.StatusMethodNotAllowed {
		t.Errorf("PATCH status = %d, want 405", status)
	}
}

func TestReceiveFileLimit(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{MaxDownloads: 1})
	content := []byte("only one")
	id := rf.begin("one.txt", len(content))

	t.Run("a second upload is refused while the first is under way", func(t *testing.T) {
		status, _, raw := rf.apiRaw(http.MethodPost, "", []byte(`{"name":"two.txt","size":1}`), true)
		if status == http.StatusCreated {
			t.Fatal("the link accepted more files than its limit")
		}
		if !strings.Contains(raw, "Not accepting files") {
			t.Error("the refusal does not say the link has stopped accepting files")
		}
	})

	t.Run("the accepted upload still finishes", func(t *testing.T) {
		if status, _ := rf.chunk(id, 0, content); status != http.StatusOK {
			t.Fatalf("chunk after the limit: status %d", status)
		}
		if status, _ := rf.finish(id); status != http.StatusOK {
			t.Fatalf("finish after the limit: status %d", status)
		}
		if rf.read("one.txt") != string(content) {
			t.Error("the file did not arrive")
		}
	})
}

func TestReceiveCancelReturnsTheSlot(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{MaxDownloads: 1})
	id := rf.begin("draft.txt", 10)
	if status, _ := rf.api(http.MethodDelete, "/"+id, nil); status != http.StatusNoContent {
		t.Fatalf("cancel: status %d, want 204", status)
	}
	rf.send("final.txt", []byte("done"))
	if rf.read("final.txt") != "done" {
		t.Error("a cancelled upload kept its place in the file limit")
	}
}

func TestReceiveWithPassword(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{PasswordHash: "h:pw"})
	body := []byte(`{"name":"secret.txt","size":1}`)
	if status, _, _ := rf.apiRaw(http.MethodPost, "", body, true); status == http.StatusCreated {
		t.Fatal("an upload started without the password")
	}
	entries, _ := os.ReadDir(rf.dir)
	if len(entries) != 0 {
		t.Fatalf("the locked link left %d files behind", len(entries))
	}

	if resp := rf.post(t, "", url.Values{"password": {"pw"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unlock: status %d, want 303", resp.StatusCode)
	}
	rf.send("secret.txt", []byte("x"))
}

func TestReceiveRefusesFilesNobodyAccepted(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{})
	status, data := rf.offer("a.txt", 5)
	if status != http.StatusCreated {
		t.Fatalf("offer: status %d", status)
	}
	id := data["id"].(string)

	cases := []struct {
		name, offer, file string
		size              int
	}{
		{"no offer", "", "a.txt", 5},
		{"an unknown offer", "nope", "a.txt", 5},
		{"a file the offer did not list", id, "b.txt", 5},
		{"a different size", id, "a.txt", 6},
	}
	for _, c := range cases {
		if status, _ := rf.startWith(c.offer, c.file, c.size); status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", c.name, status)
		}
	}
	if status, _ := rf.startWith(id, "a.txt", 5); status != http.StatusCreated {
		t.Fatalf("the accepted file: status %d", status)
	}
	if status, _ := rf.startWith(id, "a.txt", 5); status != http.StatusForbidden {
		t.Errorf("one accepted file started twice: status %d, want 403", status)
	}
}

func TestReceiveWaitsForTheOwner(t *testing.T) {
	offers := make(chan *server.Offer, 1)
	rf := newReceiveFixture(t, sharing.Spec{}, func(o *server.Offer) { offers <- o })

	status, data := rf.offer("../../trip/\x1bbeach.jpg", 3, "notes.txt", 2)
	if status != http.StatusCreated || data["state"] != "pending" {
		t.Fatalf("offer: status %d, body %v; want a pending offer", status, data)
	}
	id := data["id"].(string)
	o := <-offers
	if len(o.Files) != 2 || o.Files[0].Name != "beach.jpg" || o.Total != 5 {
		t.Errorf("the owner is shown %+v (total %d), want beach.jpg and notes.txt, 5 bytes", o.Files, o.Total)
	}
	if status, _ := rf.startWith(id, "notes.txt", 2); status != http.StatusForbidden {
		t.Fatalf("an upload started before the owner answered: status %d", status)
	}
	if entries, _ := os.ReadDir(rf.dir); len(entries) != 0 {
		t.Fatalf("%d files were written before the owner answered", len(entries))
	}

	answered := make(chan string)
	go func() {
		_, data := rf.answer(id)
		answered <- data["state"].(string)
	}()
	o.Accept()
	if state := <-answered; state != "accepted" {
		t.Fatalf("the page heard %q, want accepted", state)
	}
	if status, _ := rf.startWith(id, "notes.txt", 2); status != http.StatusCreated {
		t.Errorf("an accepted file could not start: status %d", status)
	}
}

func TestReceiveDeclinedFilesAreRefused(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{}, func(o *server.Offer) { o.Decline() })
	status, data := rf.offer("virus.exe", 10)
	if status != http.StatusCreated {
		t.Fatalf("offer: status %d", status)
	}
	id := data["id"].(string)
	if _, data := rf.answer(id); data["state"] != "declined" {
		t.Fatalf("answer = %v, want declined", data)
	}
	if status, _ := rf.startWith(id, "virus.exe", 10); status != http.StatusForbidden {
		t.Errorf("a declined file started: status %d", status)
	}
}

func TestReceiveOfferRespectsTheFileLimit(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{MaxDownloads: 2})
	if status, data := rf.offer("a", 1, "b", 1, "c", 1); status != http.StatusConflict || !strings.Contains(fmt.Sprint(data["error"]), "2 more files") {
		t.Errorf("three files for a two-file link: status %d, body %v", status, data)
	}
}

func TestReceiveLimitsQuestionsWaiting(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{}, func(*server.Offer) {})
	for i := range 3 {
		if status, _ := rf.offer("f", i); status != http.StatusCreated {
			t.Fatalf("offer %d: status %d", i, status)
		}
	}
	if status, _ := rf.offer("f", 9); status != http.StatusServiceUnavailable {
		t.Errorf("a fourth waiting offer: status %d, want 503", status)
	}
}

func TestReceiveRefusesSizesThatOverflow(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{})
	if status, data := rf.offer("a", int64(math.MaxInt64), "b", int64(math.MaxInt64)); status != http.StatusBadRequest {
		t.Errorf("sizes whose total overflows: status %d, body %v", status, data)
	}
}

func TestReceiveForgetsAnOfferOnceEveryFileArrives(t *testing.T) {
	rf := newReceiveFixture(t, sharing.Spec{})
	status, data := rf.offer("a.txt", 2)
	if status != http.StatusCreated {
		t.Fatalf("offer: status %d, body %v", status, data)
	}
	offer := data["id"].(string)
	status, data = rf.startWith(offer, "a.txt", 2)
	if status != http.StatusCreated {
		t.Fatalf("begin: status %d, body %v", status, data)
	}
	id := data["id"].(string)
	rf.chunk(id, 0, []byte("hi"))
	if status, data := rf.finish(id); status != http.StatusOK {
		t.Fatalf("finish: status %d, body %v", status, data)
	}
	if status, _ := rf.answer(offer); status != http.StatusNotFound {
		t.Errorf("an offer whose files all arrived is still held: status %d", status)
	}
}

func TestReceiveBoundsOffersHeld(t *testing.T) {
	// The fixture accepts every offer, as --yes does, so none stays pending.
	rf := newReceiveFixture(t, sharing.Spec{})
	for i := range 64 {
		if status, _ := rf.offer("f", i); status != http.StatusCreated {
			t.Fatalf("offer %d: status %d", i, status)
		}
	}
	if status, _ := rf.offer("f", 99); status != http.StatusServiceUnavailable {
		t.Errorf("an offer past the bound: status %d, want 503", status)
	}
}
