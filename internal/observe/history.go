package observe

import (
	"context"
	"fmt"
	"time"
)

// ReadHistory independently inspects a bounded prefix at first-open size. It
// never changes a live observer's cursor. The root must already be canonical.
// limited means later source bytes were not inspected, not that history ended.
func ReadHistory(ctx context.Context, root, path, agent string, options NormalizerOptions, maxRecord int, maxBytes int64, emit func(Event)) (limited bool, err error) {
	if maxBytes < 1 {
		return false, fmt.Errorf("history byte limit must be positive")
	}
	guard := Directory{root: root}
	reader, err := NewReader(path, maxRecord, DefaultBatchBytes)
	if err != nil {
		return false, err
	}
	normalizer, err := NewNormalizerWithOptions(agent, options)
	if err != nil {
		return false, err
	}
	defer normalizer.Close()
	reader.Freeze()
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := guard.safePath(path); err != nil {
			return false, err
		}
		// Make the total byte limit exact, including the final batch.
		reader.batchBytes = int(min(int64(DefaultBatchBytes), maxBytes-reader.offset))
		if reader.batchBytes <= 0 {
			return true, nil
		}
		batch, err := reader.ReadBatch()
		if err != nil {
			return false, err
		}
		for _, record := range batch.Records {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			for _, event := range normalizer.Normalize(record, time.Now()) {
				event.ObservationPhase = "history"
				emit(event)
			}
		}
		if !batch.HasMore {
			if batch.PendingBytes > 0 {
				for _, event := range normalizer.Normalize(Record{Source: reader.Location(), Diagnostic: "record.incomplete"}, time.Now()) {
					emit(event)
				}
			}
			return false, nil
		}
	}
}
