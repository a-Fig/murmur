package web

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// upload posts a send as the page does one with files (#570): a form with the recipient, the text
// and a part for each file.
func (h *harness) upload(to, text string, files ...Upload) *http.Response {
	h.t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	w.WriteField("to", to)
	w.WriteField("text", text)
	for _, u := range files {
		part, err := w.CreateFormFile("file", u.Name)
		if err != nil {
			h.t.Fatal(err)
		}
		part.Write(u.Data)
	}
	w.Close()
	req, err := http.NewRequest("POST", h.srv.URL+"/api/send", &b)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Host = "127.0.0.1:" + h.port
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// A send with files reaches the source with each file under the name the browser gave it, in
// the order they came. Files alone are a message; nothing at all is not.
func TestASendCarriesItsFiles(t *testing.T) {
	f := newFake()
	h := start(t, f, noPRs)
	resp := h.upload("quiet-otter", "the failing page", Upload{"shot one.png", []byte("\x89PNG")}, Upload{"notes.txt", []byte("see line 4")})
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("a send with files: %d %s", resp.StatusCode, b)
	}
	if sent := decode[Sent](t, resp); sent.ID != "abc123" {
		t.Errorf("what the send returned: %+v", sent)
	}
	if resp := h.upload("quiet-otter", "", Upload{"alone.txt", []byte("x")}); resp.StatusCode != http.StatusOK {
		t.Errorf("a send of a file alone: %d", resp.StatusCode)
	}
	if resp := h.upload("quiet-otter", " "); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a form with no text and no file: %d, want 400", resp.StatusCode)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	want := []string{"quiet-otter: the failing page", "file shot one.png: \x89PNG", "file notes.txt: see line 4", "quiet-otter: ", "file alone.txt: x"}
	if !reflect.DeepEqual(f.sends, want) {
		t.Errorf("the source was sent %q, want %q", f.sends, want)
	}
}

// A form larger than a message's files may be is refused, in the words the daemon would use, and
// never reaches the source.
func TestASendTooLargeIsRefused(t *testing.T) {
	f := newFake()
	h := start(t, f, noPRs)
	resp := h.upload("quiet-otter", "a recording", Upload{"big.bin", make([]byte, maxUpload)})
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "at most 20 MB") {
		t.Errorf("a form past the limit: %d %s", resp.StatusCode, b)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) != 0 {
		t.Errorf("the source was sent %q", f.sends)
	}
}

// A message's file comes as bytes to save, whatever its name claims it is: a browser that opened
// it as a page would run what a teammate's agent sent, with the page's key in reach.
func TestAFileComesAsBytesToSave(t *testing.T) {
	h := start(t, newFake(), noPRs)
	resp := h.ok("GET", "/api/file?id=abc123&i=1", "")
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "<script>alert(1)</script>" {
		t.Errorf("the file: %q", b)
	}
	for header, want := range map[string]string{
		"Content-Type":           "application/octet-stream",
		"X-Content-Type-Options": "nosniff",
		"Content-Disposition":    `attachment; filename="shot one.png"`,
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s: %q, want %q", header, got, want)
		}
	}
	host := "127.0.0.1:" + h.port
	for path, want := range map[string]int{
		"/api/file?id=abc123&i=0":  http.StatusNotFound,           // the source hasn't got it
		"/api/file?id=later&i=0":   http.StatusServiceUnavailable, // or couldn't fetch it just now
		"/api/file?id=abc123":      http.StatusBadRequest,
		"/api/file?id=abc123&i=-1": http.StatusBadRequest,
		"/api/file?id=abc123&i=x":  http.StatusBadRequest,
		"/api/file?i=1":            http.StatusBadRequest,
	} {
		if resp := h.do("GET", path, host, testToken, ""); resp.StatusCode != want {
			t.Errorf("GET %s: %d, want %d", path, resp.StatusCode, want)
		}
	}
	if resp := h.do("GET", "/api/file?id=abc123&i=1", host, "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a file asked for without the key: %d, want 401", resp.StatusCode)
	}
}
