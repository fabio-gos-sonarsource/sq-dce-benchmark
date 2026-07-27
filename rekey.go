package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

// The scanner-report protobuf is re-keyed at the wire level (no generated bindings,
// so the build stays self-contained). We only touch a few known fields and copy every
// other field verbatim, which keeps it forward/backward-compatible across versions:
//
//	Metadata:  analysis_date = 1 (varint), project_key = 3 (string),
//	           qprofiles_per_language = 7 (map<string,QProfile>; entry key=1, value=2; QProfile.key=1)
//	Component: name = 3 (string), key = 10 (string)

func appendVarintField(num int, v uint64) []byte {
	b := protowire.AppendTag(nil, protowire.Number(num), protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func appendBytesField(num int, val []byte) []byte {
	b := protowire.AppendTag(nil, protowire.Number(num), protowire.BytesType)
	return protowire.AppendBytes(b, val)
}

func decodeBytes(whole []byte) []byte {
	_, _, tl := protowire.ConsumeTag(whole)
	v, _ := protowire.ConsumeBytes(whole[tl:])
	return v
}

// eachField walks a message; fn returns the bytes to emit for each field
// (the original `whole` to keep it, modified bytes to rewrite, or nil to drop).
func eachField(b []byte, fn func(num protowire.Number, typ protowire.Type, whole []byte) []byte) []byte {
	var out []byte
	for len(b) > 0 {
		num, typ, tl := protowire.ConsumeTag(b)
		if tl < 0 {
			return append(out, b...) // unparseable tail: preserve verbatim
		}
		vl := protowire.ConsumeFieldValue(num, typ, b[tl:])
		if vl < 0 {
			return append(out, b...)
		}
		whole := b[:tl+vl]
		out = append(out, fn(num, typ, whole)...)
		b = b[tl+vl:]
	}
	return out
}

func readStringField(msg []byte, field int) string {
	found := ""
	eachField(msg, func(num protowire.Number, typ protowire.Type, whole []byte) []byte {
		if int(num) == field && typ == protowire.BytesType && found == "" {
			found = string(decodeBytes(whole))
		}
		return whole
	})
	return found
}

func setStringField(msg []byte, field int, val string) []byte {
	seen := false
	out := eachField(msg, func(num protowire.Number, typ protowire.Type, whole []byte) []byte {
		if int(num) == field && typ == protowire.BytesType {
			seen = true
			return appendBytesField(field, []byte(val))
		}
		return whole
	})
	if !seen {
		out = append(out, appendBytesField(field, []byte(val))...)
	}
	return out
}

func patchQProfileEntry(entry []byte, profiles map[string]string) ([]byte, bool) {
	lang := readStringField(entry, 1) // map key = language
	newKey, ok := profiles[lang]
	if !ok {
		return nil, false // target lacks this language's profile -> drop the entry
	}
	out := eachField(entry, func(num protowire.Number, typ protowire.Type, whole []byte) []byte {
		if num == 2 && typ == protowire.BytesType { // the QProfile value
			return appendBytesField(2, setStringField(decodeBytes(whole), 1, newKey))
		}
		return whole
	})
	return out, true
}

func patchMetadata(msg []byte, newKey string, dateMs int64, profiles map[string]string) []byte {
	seen1, seen3 := false, false
	out := eachField(msg, func(num protowire.Number, typ protowire.Type, whole []byte) []byte {
		switch {
		case num == 1 && typ == protowire.VarintType: // analysis_date
			seen1 = true
			return appendVarintField(1, uint64(dateMs))
		case num == 3 && typ == protowire.BytesType: // project_key
			seen3 = true
			return appendBytesField(3, []byte(newKey))
		case num == 7 && typ == protowire.BytesType: // qprofiles_per_language map entry
			ne, keep := patchQProfileEntry(decodeBytes(whole), profiles)
			if !keep {
				return nil
			}
			return appendBytesField(7, ne)
		default:
			return whole
		}
	})
	if !seen1 {
		out = append(out, appendVarintField(1, uint64(dateMs))...)
	}
	if !seen3 {
		out = append(out, appendBytesField(3, []byte(newKey))...)
	}
	return out
}

func patchComponent(msg []byte, oldKey, newKey string) []byte {
	return eachField(msg, func(num protowire.Number, typ protowire.Type, whole []byte) []byte {
		if num == 10 && typ == protowire.BytesType { // key
			s := string(decodeBytes(whole))
			switch {
			case s == oldKey:
				return appendBytesField(10, []byte(newKey))
			case strings.HasPrefix(s, oldKey+":"): // module/file keys (multi-module Maven/Gradle)
				return appendBytesField(10, []byte(newKey+s[len(oldKey):]))
			}
			return whole
		}
		if num == 3 && typ == protowire.BytesType { // name (root name = project key)
			if string(decodeBytes(whole)) == oldKey {
				return appendBytesField(3, []byte(newKey))
			}
		}
		return whole
	})
}

// patchReport re-keys a copied report in place so it can be replayed as newKey.
func patchReport(dir, newKey string, dateMs int64, profiles map[string]string) error {
	mdPath := filepath.Join(dir, "metadata.pb")
	md, err := os.ReadFile(mdPath)
	if err != nil {
		return err
	}
	oldKey := readStringField(md, 3)
	if err := os.WriteFile(mdPath, patchMetadata(md, newKey, dateMs, profiles), 0o644); err != nil {
		return err
	}
	comps, _ := filepath.Glob(filepath.Join(dir, "component-*.pb"))
	for _, cp := range comps {
		c, err := os.ReadFile(cp)
		if err != nil {
			return err
		}
		if err := os.WriteFile(cp, patchComponent(c, oldKey, newKey), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// stageZip copies the seed report, strips analysis caches, re-keys it, and zips it.
func stageZip(reportDir, key string, dateMs int64, profiles map[string]string, stageRoot string) (string, error) {
	d := filepath.Join(stageRoot, key)
	if err := copyTree(reportDir, d); err != nil {
		return "", err
	}
	caches, _ := filepath.Glob(filepath.Join(d, "analysis-cache*.pb")) // avoid cache-insert races on clones
	for _, c := range caches {
		os.Remove(c)
	}
	if err := patchReport(d, key, dateMs, profiles); err != nil {
		return "", err
	}
	zipPath := d + ".zip"
	if err := zipDir(d, zipPath); err != nil {
		return "", err
	}
	return zipPath, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// zipDir writes every file under dir into zipPath, with paths relative to dir (files at zip root).
func zipDir(dir, zipPath string) error {
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
}
