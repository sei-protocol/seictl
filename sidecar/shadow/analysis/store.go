package analysis

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	seis3 "github.com/sei-protocol/seictl/sidecar/s3"
)

var (
	comparePageRe      = regexp.MustCompile(`(\d+)-(\d+)\.compare\.ndjson\.gz$`)
	divergenceReportRe = regexp.MustCompile(`divergence-(\d+)\.report\.json\.gz$`)
)

// Store reads shadow comparison data from S3.
type Store struct {
	lister     seis3.ObjectLister
	downloader seis3.Downloader
	bucket     string
	prefix     string
}

// NewStore creates a Store for the given S3 location.
func NewStore(lister seis3.ObjectLister, downloader seis3.Downloader, bucket, prefix string) *Store {
	return &Store{
		lister:     lister,
		downloader: downloader,
		bucket:     bucket,
		prefix:     prefix,
	}
}

// ResolveRef converts --env/--bucket/--prefix/--region flags to concrete values.
func ResolveRef(env, bucket, prefix, region string) (resolvedBucket, resolvedPrefix, resolvedRegion string, err error) {
	switch {
	case env != "" && bucket != "":
		return "", "", "", fmt.Errorf("--env and --bucket are mutually exclusive")
	case env != "":
		resolvedBucket = env + "-sei-shadow-results"
	case bucket != "":
		resolvedBucket = bucket
	default:
		return "", "", "", fmt.Errorf("one of --env or --bucket is required")
	}
	resolvedPrefix = prefix
	if resolvedPrefix == "" {
		resolvedPrefix = "shadow-results/"
	}
	if resolvedPrefix[len(resolvedPrefix)-1] != '/' {
		resolvedPrefix += "/"
	}
	resolvedRegion = region
	if resolvedRegion == "" {
		resolvedRegion = "eu-central-1"
	}
	return
}

// s3Entry holds parsed metadata from an S3 object key.
type s3Entry struct {
	key       string
	sizeBytes int64
}

// listObjects returns all objects under the store's prefix.
func (s *Store) listObjects(ctx context.Context) ([]s3Entry, error) {
	var entries []s3Entry
	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(s.prefix),
	}
	for {
		resp, err := s.lister.ListObjectsV2(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("listing s3://%s/%s: %w", s.bucket, s.prefix, err)
		}
		for _, obj := range resp.Contents {
			entries = append(entries, s3Entry{
				key:       aws.ToString(obj.Key),
				sizeBytes: aws.ToInt64(obj.Size),
			})
		}
		if !aws.ToBool(resp.IsTruncated) {
			break
		}
		input.ContinuationToken = resp.NextContinuationToken
	}
	return entries, nil
}

// parsePages filters and parses comparison page keys from S3 entries.
// Results are sorted by start height.
func parsePages(entries []s3Entry) []PageEntry {
	var pages []PageEntry
	for _, e := range entries {
		m := comparePageRe.FindStringSubmatch(e.key)
		if len(m) < 3 {
			continue
		}
		start, err1 := strconv.ParseInt(m[1], 10, 64)
		end, err2 := strconv.ParseInt(m[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		pages = append(pages, PageEntry{
			Key:         e.key,
			StartHeight: start,
			EndHeight:   end,
			BlockCount:  int(end - start + 1),
			SizeBytes:   e.sizeBytes,
		})
	}
	sort.Slice(pages, func(i, j int) bool {
		return pages[i].StartHeight < pages[j].StartHeight
	})
	return pages
}

// parseDivergenceReports filters and parses divergence report keys.
// Results are sorted by height.
func parseDivergenceReports(entries []s3Entry) []DivergenceEntry {
	var reports []DivergenceEntry
	for _, e := range entries {
		m := divergenceReportRe.FindStringSubmatch(e.key)
		if len(m) < 2 {
			continue
		}
		height, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		reports = append(reports, DivergenceEntry{
			Key:       e.key,
			Height:    height,
			SizeBytes: e.sizeBytes,
		})
	}
	sort.Slice(reports, func(i, j int) bool {
		return reports[i].Height < reports[j].Height
	})
	return reports
}

// List returns an inventory of comparison data in S3.
func (s *Store) List(ctx context.Context, input ListInput) (*ListOutput, error) {
	entries, err := s.listObjects(ctx)
	if err != nil {
		return nil, err
	}

	pages := parsePages(entries)
	reports := parseDivergenceReports(entries)

	// Filter by height range.
	if input.Start != nil || input.End != nil {
		pages = filterPages(pages, input.Start, input.End)
		reports = filterReports(reports, input.Start, input.End)
	}

	// Build summary from full dataset (before truncation).
	summary := DataInventory{
		TotalPages:             len(pages),
		TotalDivergenceReports: len(reports),
	}
	if len(pages) > 0 {
		summary.MinHeight = pages[0].StartHeight
		summary.MaxHeight = pages[len(pages)-1].EndHeight
		for _, p := range pages {
			summary.TotalBlocksCovered += int64(p.BlockCount)
		}
	}

	// Apply limit to returned entries.
	limit := input.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if len(pages) > limit {
		pages = pages[:limit]
	}
	if len(reports) > limit {
		reports = reports[:limit]
	}

	return &ListOutput{
		Pages:             pages,
		DivergenceReports: reports,
		Summary:           summary,
	}, nil
}

func filterPages(pages []PageEntry, start, end *int64) []PageEntry {
	var filtered []PageEntry
	for _, p := range pages {
		if start != nil && p.EndHeight < *start {
			continue
		}
		if end != nil && p.StartHeight > *end {
			continue
		}
		filtered = append(filtered, p)
	}
	return filtered
}

func filterReports(reports []DivergenceEntry, start, end *int64) []DivergenceEntry {
	var filtered []DivergenceEntry
	for _, r := range reports {
		if start != nil && r.Height < *start {
			continue
		}
		if end != nil && r.Height > *end {
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered
}

// pagesOverlapping returns page keys that overlap [start, end], sorted by start height,
// capped at maxPages. Returns the count of total matching pages (before cap).
func (s *Store) pagesOverlapping(ctx context.Context, start, end int64, maxPages int) (keys []string, totalMatching int, err error) {
	entries, err := s.listObjects(ctx)
	if err != nil {
		return nil, 0, err
	}

	pages := parsePages(entries)
	startPtr := &start
	endPtr := &end
	pages = filterPages(pages, startPtr, endPtr)

	totalMatching = len(pages)
	if maxPages > 0 && len(pages) > maxPages {
		pages = pages[:maxPages]
	}

	keys = make([]string, len(pages))
	for i, p := range pages {
		keys[i] = p.Key
	}
	return keys, totalMatching, nil
}
