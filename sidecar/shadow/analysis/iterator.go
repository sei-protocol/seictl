package analysis

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
	"github.com/sei-protocol/seictl/sidecar/shadow"
)

// PageIterator streams CompareResults from S3 comparison pages.
// Downloads pages sequentially, decompresses gzip, and decodes NDJSON
// line-by-line. Results outside [start, end] are skipped internally.
type PageIterator struct {
	ctx        context.Context
	downloader seis3.Downloader
	bucket     string
	keys       []string
	keyIdx     int
	start      int64
	end        int64
	body       io.ReadCloser
	gzReader   io.ReadCloser
	decoder    *json.Decoder
	pageCount  int
}

func newPageIterator(ctx context.Context, downloader seis3.Downloader, bucket string, keys []string, start, end int64) *PageIterator {
	return &PageIterator{
		ctx:        ctx,
		downloader: downloader,
		bucket:     bucket,
		keys:       keys,
		start:      start,
		end:        end,
	}
}

// Next returns the next CompareResult within [start, end].
// Returns io.EOF when all pages are exhausted.
func (p *PageIterator) Next() (*shadow.CompareResult, error) {
	for {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}

		if p.decoder == nil {
			if err := p.openNextPage(); err != nil {
				if err == io.EOF {
					return nil, io.EOF
				}
				// Page download failed — warn and skip to next page.
				fmt.Fprintf(os.Stderr, "warning: %v, skipping page\n", err)
				continue
			}
		}

		var result shadow.CompareResult
		if err := p.decoder.Decode(&result); err != nil {
			if err == io.EOF {
				p.closeCurrent()
				continue
			}
			fmt.Fprintf(os.Stderr, "skipping malformed NDJSON line: %v\n", err)
			continue
		}

		if result.Timestamp == "" {
			fmt.Fprintf(os.Stderr, "skipping line at height %d: empty timestamp (wrong NDJSON format?)\n", result.Height)
			continue
		}

		if result.Height < p.start {
			continue
		}
		if result.Height > p.end {
			p.closeCurrent()
			p.keyIdx = len(p.keys) // no subsequent page can be in range
			return nil, io.EOF
		}

		return &result, nil
	}
}

// PageCount returns the number of pages downloaded so far.
func (p *PageIterator) PageCount() int {
	return p.pageCount
}

// Close releases the current page reader.
func (p *PageIterator) Close() error {
	p.closeCurrent()
	return nil
}

func (p *PageIterator) openNextPage() error {
	if p.keyIdx >= len(p.keys) {
		return io.EOF
	}

	key := p.keys[p.keyIdx]
	p.keyIdx++

	resp, err := p.downloader.GetObject(p.ctx, &s3.GetObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("downloading %s: %w", key, err)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		resp.Body.Close()
		return fmt.Errorf("decompressing %s: %w", key, err)
	}

	p.body = resp.Body
	p.gzReader = gz
	p.decoder = json.NewDecoder(gz)
	p.pageCount++
	return nil
}

func (p *PageIterator) closeCurrent() {
	if p.gzReader != nil {
		p.gzReader.Close()
		p.gzReader = nil
	}
	if p.body != nil {
		p.body.Close()
		p.body = nil
	}
	p.decoder = nil
}

// OpenRange returns a PageIterator over comparison pages overlapping [start, end].
// maxPages caps how many S3 pages are downloaded; -1 means unlimited.
func (s *Store) OpenRange(ctx context.Context, start, end int64, maxPages int) (*PageIterator, int, error) {
	keys, totalMatching, err := s.pagesOverlapping(ctx, start, end, maxPages)
	if err != nil {
		return nil, 0, err
	}
	return newPageIterator(ctx, s.downloader, s.bucket, keys, start, end), totalMatching, nil
}
