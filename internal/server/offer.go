package server

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/AliJabbar034/vrok/internal/inbox"
)

const (
	// offerPath is where a sender asks to send a batch of files.
	offerPath = "_offer"

	// offerTimeout is how long an offer waits for the owner's answer.
	offerTimeout = 5 * time.Minute
	// offerPoll is the longest one status request is held open waiting for
	// the answer, well under the 100 seconds a Cloudflare tunnel allows.
	offerPoll = 25 * time.Second
	// offerLinger keeps a declined or expired offer around long enough for
	// the page to learn the answer.
	offerLinger = time.Minute

	// maxOfferFiles and maxOfferBytes bound one offer.
	maxOfferFiles = 500
	maxOfferBytes = 256 << 10
	// maxPendingOffers bounds the questions waiting on the owner, so one
	// visitor cannot bury the terminal in prompts.
	maxPendingOffers = 3
	// maxOffers bounds every offer held at once, answered or not. An accepted
	// offer is kept until its last file arrives, and with --yes every offer
	// is accepted, so without a bound a visitor could grow memory forever.
	maxOffers = 64
)

// OfferFile is one file in an offer, as the owner is shown it.
type OfferFile struct {
	// Name is made safe to print: no path, no control characters.
	Name string
	Size int64
}

// Offer is a batch of files a visitor asks to send. Nothing is written until
// the owner accepts it, and then only the files it lists, at the sizes it
// gave: an upload that was not offered and accepted is refused.
type Offer struct {
	Files []OfferFile
	Total int64

	mu      sync.Mutex
	state   string
	decided chan struct{}
	// left counts the accepted files not yet started, by the name and size
	// the sender gave.
	left map[offerKey]int
	// arriving counts the files not yet received completely.
	arriving int
}

type offerKey struct {
	name string
	size int64
}

const (
	offerPending  = "pending"
	offerAccepted = "accepted"
	offerDeclined = "declined"
	offerExpired  = "expired"
)

func newOffer(names []string, sizes []int64) *Offer {
	o := &Offer{state: offerPending, decided: make(chan struct{}), left: make(map[offerKey]int), arriving: len(names)}
	for i, name := range names {
		o.Files = append(o.Files, OfferFile{Name: inbox.SafeName(name), Size: sizes[i]})
		o.Total += sizes[i]
		o.left[offerKey{name, sizes[i]}]++
	}
	return o
}

// Accept lets the files in. It does nothing once the offer is answered.
func (o *Offer) Accept() { o.decide(offerAccepted) }

// Decline refuses the files. It does nothing once the offer is answered.
func (o *Offer) Decline() { o.decide(offerDeclined) }

// Decided is closed once the offer is accepted, declined or expires.
func (o *Offer) Decided() <-chan struct{} { return o.decided }

// Expired reports whether the offer ended without an answer.
func (o *Offer) Expired() bool { return o.State() == offerExpired }

// State is "pending", "accepted", "declined" or "expired".
func (o *Offer) State() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state
}

func (o *Offer) decide(state string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.state != offerPending {
		return false
	}
	o.state = state
	close(o.decided)
	return true
}

// take uses up one accepted file of this name and size.
func (o *Offer) take(name string, size int64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	key := offerKey{name, size}
	if o.state != offerAccepted || o.left[key] == 0 {
		return false
	}
	o.left[key]--
	return true
}

// received counts one file of this offer as arrived, and reports whether it
// was the last.
func (o *Offer) received() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.arriving--
	return o.arriving <= 0
}

// giveBack returns a file whose upload could not start, so the sender can
// try it again.
func (o *Offer) giveBack(name string, size int64) {
	o.mu.Lock()
	o.left[offerKey{name, size}]++
	o.mu.Unlock()
}

// addOffer registers an offer, refusing it when too many are already
// waiting for an answer, or held at all.
func (rc *receiver) addOffer(o *Offer) (string, bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.offers) >= maxOffers {
		return "", false
	}
	pending := 0
	for _, other := range rc.offers {
		if other.State() == offerPending {
			pending++
		}
	}
	if pending >= maxPendingOffers {
		return "", false
	}
	id := newOfferID()
	rc.offers[id] = o

	expire := time.AfterFunc(offerTimeout, func() { o.decide(offerExpired) })
	go func() {
		<-o.decided
		expire.Stop()
		// An accepted offer stays while its files are sent; any other answer
		// only needs to reach the page.
		if o.State() != offerAccepted {
			time.AfterFunc(offerLinger, func() { rc.removeOffer(id) })
		}
	}()
	return id, true
}

func (rc *receiver) offer(id string) *Offer {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.offers[id]
}

func (rc *receiver) removeOffer(id string) {
	rc.mu.Lock()
	delete(rc.offers, id)
	rc.mu.Unlock()
}

// declineAll answers every waiting offer when the share closes, so no page
// is left waiting for a terminal that has gone.
func (rc *receiver) declineAll() {
	rc.mu.Lock()
	offers := make([]*Offer, 0, len(rc.offers))
	for _, o := range rc.offers {
		offers = append(offers, o)
	}
	rc.mu.Unlock()
	for _, o := range offers {
		o.Decline()
	}
}

func newOfferID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}
