package mynet

import (
	"os"
	"strings"
)

// fileNonEmpty 是否存在且非空（仅探测，不读内容）。
func fileNonEmpty(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Size() > 0
}

// readKV 读简单 key=value 文件（mynet.conf 等）。
func readKV(p string) map[string]string {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if i := strings.IndexByte(ln, '='); i > 0 {
			out[strings.TrimSpace(ln[:i])] = strings.TrimSpace(ln[i+1:])
		}
	}
	return out
}
