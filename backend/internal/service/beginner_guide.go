package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const SettingKeyBeginnerGuide = "beginner_guide_v1"

var ErrBeginnerGuideConflict = infraerrors.Conflict("BEGINNER_GUIDE_CONFLICT", "the guide was updated by another editor; reload before saving")

type BeginnerGuideChapter struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Markdown    string `json:"markdown"`
}

type BeginnerGuideContent map[string]map[string]BeginnerGuideChapter

type BeginnerGuideDocument struct {
	Revision    int64                `json:"revision"`
	Draft       BeginnerGuideContent `json:"draft"`
	Published   BeginnerGuideContent `json:"published"`
	UpdatedAt   *time.Time           `json:"updated_at"`
	PublishedAt *time.Time           `json:"published_at"`
}

type PublishedBeginnerGuide struct {
	Content     BeginnerGuideContent `json:"content"`
	PublishedAt *time.Time           `json:"published_at"`
}

func decodeBeginnerGuide(raw string) (*BeginnerGuideDocument, error) {
	doc := &BeginnerGuideDocument{Draft: BeginnerGuideContent{}, Published: BeginnerGuideContent{}}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), doc); err != nil {
			return nil, fmt.Errorf("decode beginner guide: %w", err)
		}
	}
	if doc.Draft == nil {
		doc.Draft = BeginnerGuideContent{}
	}
	if doc.Published == nil {
		doc.Published = BeginnerGuideContent{}
	}
	return doc, nil
}

func validateBeginnerGuide(content BeginnerGuideContent) error {
	chapters := map[string]bool{"quickstart": true, "codex": true, "vscode": true, "claude": true, "ccswitch": true, "chat": true, "models": true, "billing": true, "troubleshooting": true}
	for locale, items := range content {
		if locale != "zh" && locale != "en" {
			return fmt.Errorf("unsupported guide language: %s", locale)
		}
		for id, item := range items {
			if !chapters[id] {
				return fmt.Errorf("unknown guide chapter: %s", id)
			}
			if strings.TrimSpace(item.Title) == "" || utf8.RuneCountInString(item.Title) > 120 {
				return fmt.Errorf("chapter title must contain 1 to 120 characters")
			}
			if utf8.RuneCountInString(item.Description) > 400 {
				return fmt.Errorf("chapter description cannot exceed 400 characters")
			}
			if strings.TrimSpace(item.Markdown) == "" || len(item.Markdown) > 100000 {
				return fmt.Errorf("chapter Markdown must contain 1 to 100000 bytes")
			}
		}
	}
	return nil
}

func (s *SettingService) GetBeginnerGuide(ctx context.Context) (*BeginnerGuideDocument, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyBeginnerGuide)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, err
	}
	return decodeBeginnerGuide(raw)
}

func (s *SettingService) UpdateBeginnerGuide(ctx context.Context, revision int64, content BeginnerGuideContent, publish bool) (*BeginnerGuideDocument, error) {
	if err := validateBeginnerGuide(content); err != nil {
		return nil, infraerrors.BadRequest("BEGINNER_GUIDE_INVALID", err.Error())
	}
	repo, ok := s.settingRepo.(AtomicSettingUpdater)
	if !ok {
		return nil, fmt.Errorf("beginner guide requires atomic settings updates")
	}
	var result *BeginnerGuideDocument
	err := repo.UpdateValue(ctx, SettingKeyBeginnerGuide, func(raw string) (string, error) {
		doc, err := decodeBeginnerGuide(raw)
		if err != nil {
			return "", err
		}
		if doc.Revision != revision {
			return "", ErrBeginnerGuideConflict
		}
		if content == nil {
			content = BeginnerGuideContent{}
		}
		now := time.Now().UTC()
		doc.Draft = content
		doc.Revision++
		doc.UpdatedAt = &now
		if publish {
			doc.Published = content
			doc.PublishedAt = &now
		}
		encoded, err := json.Marshal(doc)
		result = doc
		return string(encoded), err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
