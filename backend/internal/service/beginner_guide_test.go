package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type beginnerGuideSettingRepo struct {
	SettingRepository
	mu    sync.Mutex
	value string
	err   error
}

func (r *beginnerGuideSettingRepo) GetValue(context.Context, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return "", r.err
	}
	if r.value == "" {
		return "", ErrSettingNotFound
	}
	return r.value, nil
}

func (r *beginnerGuideSettingRepo) UpdateValue(_ context.Context, _ string, update func(string) (string, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	value, err := update(r.value)
	if err == nil {
		r.value = value
	}
	return err
}

func guideTestContent(title string) BeginnerGuideContent {
	return BeginnerGuideContent{"zh": {"quickstart": {Title: title, Description: "A guide", Markdown: "## Start\n\n{{api_root}}"}}}
}

func TestBeginnerGuideDraftPublishRestore(t *testing.T) {
	ctx := context.Background()
	svc := &SettingService{settingRepo: &beginnerGuideSettingRepo{}}
	doc, err := svc.GetBeginnerGuide(ctx)
	require.NoError(t, err)
	require.Empty(t, doc.Draft)
	require.Empty(t, doc.Published)

	doc, err = svc.UpdateBeginnerGuide(ctx, 0, guideTestContent("First draft"), false)
	require.NoError(t, err)
	require.EqualValues(t, 1, doc.Revision)
	require.NotNil(t, doc.UpdatedAt)
	require.Nil(t, doc.PublishedAt)
	require.Empty(t, doc.Published)

	doc, err = svc.UpdateBeginnerGuide(ctx, doc.Revision, doc.Draft, true)
	require.NoError(t, err)
	require.Equal(t, doc.Draft, doc.Published)
	require.NotNil(t, doc.PublishedAt)
	publishedAt := *doc.PublishedAt

	doc, err = svc.UpdateBeginnerGuide(ctx, doc.Revision, guideTestContent("Unpublished revision"), false)
	require.NoError(t, err)
	require.Equal(t, "First draft", doc.Published["zh"]["quickstart"].Title)
	require.Equal(t, publishedAt, *doc.PublishedAt)

	_, err = svc.UpdateBeginnerGuide(ctx, 1, guideTestContent("Stale update"), true)
	require.ErrorIs(t, err, ErrBeginnerGuideConflict)
	doc, err = svc.GetBeginnerGuide(ctx)
	require.NoError(t, err)
	require.Equal(t, "Unpublished revision", doc.Draft["zh"]["quickstart"].Title)

	doc, err = svc.UpdateBeginnerGuide(ctx, doc.Revision, BeginnerGuideContent{}, true)
	require.NoError(t, err)
	require.Empty(t, doc.Draft)
	require.Empty(t, doc.Published)
	doc, err = svc.GetBeginnerGuide(ctx)
	require.NoError(t, err)
	require.Empty(t, doc.Published)
}

func TestBeginnerGuideConcurrentEditors(t *testing.T) {
	svc := &SettingService{settingRepo: &beginnerGuideSettingRepo{}}
	var success, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.UpdateBeginnerGuide(context.Background(), 0, guideTestContent("Concurrent"), true)
			if err == nil {
				success.Add(1)
			} else if errors.Is(err, ErrBeginnerGuideConflict) {
				conflicts.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, success.Load())
	require.EqualValues(t, 7, conflicts.Load())
}

func TestBeginnerGuideValidation(t *testing.T) {
	cases := map[string]BeginnerGuideContent{
		"locale":             {"fr": {}},
		"chapter":            {"zh": {"unknown": {Title: "Title", Markdown: "Text"}}},
		"empty title":        {"zh": {"codex": {Title: " ", Markdown: "Text"}}},
		"title length":       {"zh": {"codex": {Title: strings.Repeat("中", 121), Markdown: "Text"}}},
		"description length": {"en": {"codex": {Title: "Title", Description: strings.Repeat("a", 401), Markdown: "Text"}}},
		"empty markdown":     {"zh": {"codex": {Title: "Title", Markdown: "\n "}}},
		"markdown bytes":     {"zh": {"codex": {Title: "Title", Markdown: strings.Repeat("中", 33334)}}},
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &beginnerGuideSettingRepo{}
			svc := &SettingService{settingRepo: repo}
			_, err := svc.UpdateBeginnerGuide(context.Background(), 0, content, true)
			require.Error(t, err)
			require.Empty(t, repo.value)
		})
	}
	require.NoError(t, validateBeginnerGuide(guideTestContent(strings.Repeat("中", 120))))
}

func TestBeginnerGuideReadFailures(t *testing.T) {
	ctx := context.Background()
	repo := &beginnerGuideSettingRepo{value: "invalid JSON"}
	svc := &SettingService{settingRepo: repo}
	_, err := svc.GetBeginnerGuide(ctx)
	require.Error(t, err)
	_, err = svc.UpdateBeginnerGuide(ctx, 0, guideTestContent("Valid"), true)
	require.Error(t, err)
	require.Equal(t, "invalid JSON", repo.value)
	repo.err = errors.New("database unavailable")
	_, err = svc.GetBeginnerGuide(ctx)
	require.ErrorIs(t, err, repo.err)
}
