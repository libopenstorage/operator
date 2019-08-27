package log

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"runtime"
	"strings"
)

func init() {
	log.SetReportCaller(true)
	formatter := &log.TextFormatter{
		TimestampFormat: "02-01-2006 15:04:05",
		FullTimestamp:   true,
		CallerPrettyfier: func(f *runtime.Frame) (string, string) {
			return "", fmt.Sprintf(" %s:%d", formatFilePath(f.File), f.Line)
		},
	}
	log.SetFormatter(formatter)
}

func formatFilePath(path string) string {
	arr := strings.Split(path, "/")
	return arr[len(arr)-1]
}
