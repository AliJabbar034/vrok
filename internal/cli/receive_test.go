package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/AliJabbar034/vrok/internal/server"
	"github.com/AliJabbar034/vrok/internal/sharing"
	"github.com/AliJabbar034/vrok/internal/ui"
)

func TestReceiveFolderDefaultsToDownloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got, err := receiveFolder(nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "Downloads", "vrok"); got != want {
		t.Errorf("receiveFolder() = %q, want %q", got, want)
	}
	if got, _ := receiveFolder([]string{"./inbox"}); got != "./inbox" {
		t.Errorf("receiveFolder(./inbox) = %q, want the folder given", got)
	}
}

func TestDisplayPathShortensHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	sep := string(os.PathSeparator)
	cases := map[string]string{
		filepath.Join(home, "Downloads", "vrok"): "~" + sep + filepath.Join("Downloads", "vrok"),
		home:                                     "~",
		filepath.Dir(home):                       filepath.Dir(home),
	}
	for in, want := range cases {
		if got := displayPath(in); got != want {
			t.Errorf("displayPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOffersAreAnsweredOneAtATime(t *testing.T) {
	var out bytes.Buffer
	s := &sharer{app: &app{printer: ui.New(&out, &out)}, opts: shareOptions{receive: true}}
	s.app.printer.SetColor(false)

	first, second := offerOf(t, "a.txt"), offerOf(t, "b.txt")
	s.onOffer(first)
	s.onOffer(second)
	if strings.Contains(out.String(), "b.txt") {
		t.Fatalf("the second offer was shown while the first was waiting:\n%s", out.String())
	}

	if !s.answerOffer(false) {
		t.Fatal("there was an offer to answer")
	}
	if first.State() != "declined" || second.State() != "pending" {
		t.Fatalf("n answered first=%s second=%s, want the first declined only", first.State(), second.State())
	}
	if !strings.Contains(out.String(), "b.txt") {
		t.Fatalf("the next offer was not shown after answering:\n%s", out.String())
	}

	s.answerOffer(true)
	if second.State() != "accepted" {
		t.Fatalf("y left the second offer %s", second.State())
	}
	if s.answerOffer(true) {
		t.Error("y with nothing waiting reported an answer")
	}
}

// offerOf builds a real offer by sending one to a receive server.
func offerOf(t *testing.T, name string) *server.Offer {
	t.Helper()
	got := make(chan *server.Offer, 1)
	registry := sharing.NewRegistry()
	share := sharing.New(sharing.Spec{ID: "id", Token: "tok", Kind: sharing.KindReceive, Root: t.TempDir(), CreatedAt: time.Now()})
	if err := registry.Add(share); err != nil {
		t.Fatal(err)
	}
	key, _ := security.NewSecret()
	srv, err := server.New(server.Options{
		Resolver: registry,
		Signer:   security.NewHMACSigner(key),
		Hasher:   security.NewArgon2Hasher(security.DefaultArgon2Params()),
		OnOffer:  func(_ sharing.Spec, o *server.Offer) { got <- o },
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+server.SharePath("tok")+"_offer",
		strings.NewReader(`{"files":[{"name":"`+name+`","size":1}]}`))
	req.Header.Set("X-Vrok-Upload", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return <-got
}
