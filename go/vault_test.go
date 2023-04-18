package sshvault

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/kr/pty"
	"github.com/ssh-vault/crypto"
	"github.com/ssh-vault/crypto/aead"
)

// These are done in one function to avoid declaring global variables in a test
// file.
func TestVaultFunctions(t *testing.T) {
	dir, err := ioutil.TempDir("", "vault")
	if err != nil {
		t.Error(err)
	}
	defer os.RemoveAll(dir) // clean up

	tmpfile := filepath.Join(dir, "vault")

	vault, err := New("", "test_data/id_rsa.pub", "", "create", tmpfile)
	if err != nil {
		t.Error(err)
	}

	keyPw := string("argle-bargle\n")
	pty, tty, err := pty.Open()
	if err != nil {
		t.Errorf("Unable to open pty: %s", err)
	}

	// File Descriptor magic. GetPasswordPrompt() reads the password
	// from stdin. For the test, we save stdin to a spare FD,
	// point stdin at the file, run the system under test, and
	// finally restore the original stdin
	oldStdin, _ := syscall.Dup(int(syscall.Stdin))
	oldStdout, _ := syscall.Dup(int(syscall.Stdout))
	syscall.Dup2(int(tty.Fd()), int(syscall.Stdin))
	syscall.Dup2(int(tty.Fd()), int(syscall.Stdout))

	go PtyWriteback(pty, keyPw)

	keyPwTest, err := vault.GetPasswordPrompt()

	syscall.Dup2(oldStdin, int(syscall.Stdin))
	syscall.Dup2(oldStdout, int(syscall.Stdout))

	if err != nil {
		t.Error(err)
	}
	if string(strings.Trim(keyPw, "\n")) != string(keyPwTest) {
		t.Errorf("password prompt: expected %s, got %s\n", keyPw, keyPwTest)
	}

	PKCS8, err := vault.PKCS8()
	if err != nil {
		t.Error(err)
	}

	vault.PublicKey, err = vault.GetRSAPublicKey(PKCS8)
	if err != nil {
		t.Error(err)
	}

	vault.Fingerprint, err = vault.GenFingerprint(PKCS8)
	if err != nil {
		t.Error(err)
	}

	if vault.Password, err = crypto.GenerateNonce(32); err != nil {
		t.Error(err)
	}

	// Skip vault.Create because we don't need/want to interact with an editor
	// for tests.
	in := []byte("The quick brown fox jumps over the lazy dog")

	out, err := aead.Encrypt(vault.Password, in, []byte(vault.Fingerprint))
	if err != nil {
		t.Error(err)
	}

	if err = vault.Close(out); err != nil {
		t.Error(err)
	}

	enc1, err := ioutil.ReadFile(tmpfile)
	if err != nil {
		t.Error(err)
	}

	plaintext, err := vault.View()
	if err != nil {
		t.Error(err)
	}

	if !bytes.Equal(in, plaintext) {
		t.Error("in != out")
	}

	os.Setenv("EDITOR", "cat")
	edited, err := vault.Edit(plaintext)
	if err != nil {
		t.Error(err)
	}

	out, err = aead.Encrypt(vault.Password, edited, []byte(vault.Fingerprint))
	if err != nil {
		t.Error(err)
	}

	if err = vault.Close(out); err != nil {
		t.Error(err)
	}

	plaintext, err = vault.View()
	if err != nil {
		t.Error(err)
	}

	enc2, err := ioutil.ReadFile(tmpfile)
	if err != nil {
		t.Error(err)
	}

	if !bytes.Equal(edited, plaintext) {
		t.Error("edited != plaintext ")
	}

	if bytes.Equal(enc1, enc2) {
		t.Error("Expecting different encrypted outputs")
	}
}

func TestVaultFunctionsSTDOUT(t *testing.T) {
	dir, err := ioutil.TempDir("", "vault")
	if err != nil {
		t.Error(err)
	}
	defer os.RemoveAll(dir) // clean up

	vault, err := New("", "test_data/id_rsa.pub", "", "create", "")
	if err != nil {
		t.Error(err)
	}

	PKCS8, err := vault.PKCS8()
	if err != nil {
		t.Error(err)
	}

	vault.PublicKey, err = vault.GetRSAPublicKey(PKCS8)
	if err != nil {
		t.Error(err)
	}

	vault.Fingerprint, err = vault.GenFingerprint(PKCS8)
	if err != nil {
		t.Error(err)
	}

	if vault.Password, err = crypto.GenerateNonce(32); err != nil {
		t.Error(err)
	}

	// Skip vault.Create because we don't need/want to interact with an editor
	in := []byte("The quick brown fox jumps over the lazy dog")

	out, err := aead.Encrypt(vault.Password, in, []byte(vault.Fingerprint))
	if err != nil {
		t.Error(err)
	}

	rescueStdout := os.Stdout // keep backup of the real stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	if err = vault.Close(out); err != nil {
		t.Error(err)
	}

	w.Close()
	outStdout, _ := ioutil.ReadAll(r)
	os.Stdout = rescueStdout
	tmpfile, err := ioutil.TempFile("", "stdout")
	if err != nil {
		t.Error(err)
	}
	tmpfile.Write([]byte(outStdout))
	vault.vault = tmpfile.Name()

	plaintext, err := vault.View()
	if err != nil {
		t.Error(err)
	}

	if !bytes.Equal(in, plaintext) {
		t.Error("in != out")
	}
}

func TestVaultNew(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expect(t, "ssh-vault", r.Header.Get("User-agent"))
		fmt.Fprintln(w, "ssh-rsa ABC")
	}))
	defer ts.Close()
	_, err := New("", "", ts.URL, "view", "")
	if err != nil {
		t.Error(err)
	}
}

func TestVaultNewNoKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expect(t, "ssh-vault", r.Header.Get("User-agent"))
	}))
	defer ts.Close()
	_, err := New("", "", ts.URL, "view", "")
	if err == nil {
		t.Error("Expecting error")
	}
}

func TestVaultNoKey(t *testing.T) {
	_, err := New("", "/dev/null/none", "", "", "")
	if err == nil {
		t.Error("Expecting error")
	}
}

func TestExistingVault(t *testing.T) {
	_, err := New("", "test_data/id_rsa.pub", "", "create", "LICENSE")
	if err == nil {
		t.Error("Expecting error")
	}
}

func TestPKCS8(t *testing.T) {
	v := &vault{
		key: "/dev/null/non-existent",
	}
	_, err := v.PKCS8()
	if err == nil {
		t.Error("Expecting error")
	}
}

func TestKeyHTTPNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expect(t, "ssh-vault", r.Header.Get("User-agent"))
	}))
	defer ts.Close()
	_, err := New("", ts.URL, "", "view", "")
	if err == nil {
		t.Error("Expecting error")
	}
}
