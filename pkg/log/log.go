package log

import (
	"fmt"
	"runtime"

	log "github.com/sirupsen/logrus"
)

func init() {
	log.SetReportCaller(true)
	formatter := &log.TextFormatter{
		TimestampFormat: "02-01-2006 15:04:05",
		FullTimestamp:   true,
		CallerPrettyfier: func(f *runtime.Frame) (string, string) {
			return fmt.Sprintf(" %s: line:%d", formatFilePath(f.File), f.Line), ""
		},
	}
	log.SetFormatter(formatter)
}

func formatFilePath(path string) string {
	for i, c := range path {
		if c != 'g' {
			continue
		}
		if i+5 < len(path) {
			if path[i:i+6] == "github" {
				return path[i:]
			}
		}
	}
	return path
}
