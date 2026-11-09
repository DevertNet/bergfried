package update

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestReportWithoutCallbackIsNoop(t *testing.T) {
	report(context.Background(), "ignored %d", 1) // must not panic
}

func TestProgressReaderReportsDownload(t *testing.T) {
	var got []string
	ctx := WithReport(context.Background(), func(m string) { got = append(got, m) })
	pr := &progressReader{r: strings.NewReader(strings.Repeat("x", 2000)), ctx: ctx, label: "Downloading the update", total: 2000, last: time.Now().Add(-time.Second)}
	if _, err := io.Copy(io.Discard, pr); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || !strings.Contains(got[0], "Downloading the update") || !strings.Contains(got[0], "%") {
		t.Fatalf("unexpected progress messages: %q", got)
	}
}
