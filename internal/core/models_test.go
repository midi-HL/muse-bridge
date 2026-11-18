package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midi-HL/muse-bridge/internal/media"
)

func TestDownloadedModelChecksumAndRelativePath(t *testing.T) {
	s, _ := testService(t)
	data := []byte("synthetic model bytes")
	if _, err := s.storeDownloadedModel("model.onnx", data, strings.Repeat("0", 64)); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, "models")); !os.IsNotExist(err) {
		t.Fatal("checksum failure created model artifacts")
	}
	out, err := s.storeDownloadedModel(`..\model:payload.onnx`, data, strings.ToUpper(media.SHA256(data)))
	if err != nil {
		t.Fatal(err)
	}
	rel := out["path"].(string)
	if filepath.IsAbs(rel) || !strings.HasPrefix(rel, "models/") || strings.Contains(rel, s.dataDir) || strings.Contains(rel, ":") {
		t.Fatal("model result disclosed host path or unsafe name")
	}
	b, err := os.ReadFile(filepath.Join(s.dataDir, filepath.FromSlash(rel)))
	if err != nil || string(b) != string(data) {
		t.Fatal("model file content not saved")
	}
}
