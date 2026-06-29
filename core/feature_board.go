package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const featureBoardRelPath = "board/features.json"

type FeatureTask struct {
	TaskID        string    `json:"task_id"`
	Title         string    `json:"title"`
	Owner         string    `json:"owner"`
	Status        string    `json:"status"`
	RepoWorktree  string    `json:"repo_worktree"`
	Blocker       string    `json:"blocker"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	Evidence      []string  `json:"evidence"`
	HandbackState string    `json:"handback_state"`
	NextAction    string    `json:"next_action"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type FeatureBoard struct {
	Tasks []*FeatureTask `json:"tasks"`
}

type FeatureBoardStore struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

func NewFeatureBoardStore(dataDir string) *FeatureBoardStore {
	return &FeatureBoardStore{
		path: filepath.Join(dataDir, featureBoardRelPath),
		now:  time.Now,
	}
}

func (s *FeatureBoardStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *FeatureBoardStore) Create(title, owner, repoWorktree, nextAction string) (*FeatureTask, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil, fmt.Errorf("feature board store is not configured")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("feature title is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	board, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	task := &FeatureTask{
		TaskID:        uniqueFeatureTaskID(board, now, title),
		Title:         title,
		Owner:         strings.TrimSpace(owner),
		Status:        "planning",
		RepoWorktree:  strings.TrimSpace(repoWorktree),
		Blocker:       "",
		LastHeartbeat: now,
		Evidence:      []string{},
		HandbackState: "not_started",
		NextAction:    strings.TrimSpace(nextAction),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	board.Tasks = append(board.Tasks, task)
	if err := s.saveLocked(board); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *FeatureBoardStore) loadLocked() (*FeatureBoard, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &FeatureBoard{Tasks: []*FeatureTask{}}, nil
		}
		return nil, err
	}
	var board FeatureBoard
	if len(strings.TrimSpace(string(data))) == 0 {
		return &FeatureBoard{Tasks: []*FeatureTask{}}, nil
	}
	if err := json.Unmarshal(data, &board); err != nil {
		return nil, err
	}
	if board.Tasks == nil {
		board.Tasks = []*FeatureTask{}
	}
	return &board, nil
}

func (s *FeatureBoardStore) saveLocked(board *FeatureBoard) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(board, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return AtomicWriteFile(s.path, data, 0o644)
}

func uniqueFeatureTaskID(board *FeatureBoard, now time.Time, title string) string {
	base := "feat-" + now.Format("20060102-150405")
	if slug := featureTitleSlug(title); slug != "" {
		base += "-" + slug
	}
	used := make(map[string]bool)
	if board != nil {
		for _, task := range board.Tasks {
			if task != nil {
				used[task.TaskID] = true
			}
		}
	}
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !used[candidate] {
			return candidate
		}
	}
}

var featureSlugCleanup = regexp.MustCompile(`[^a-z0-9]+`)

func featureTitleSlug(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	title = featureSlugCleanup.ReplaceAllString(title, "-")
	title = strings.Trim(title, "-")
	if title == "" {
		return ""
	}
	parts := strings.Split(title, "-")
	if len(parts) > 4 {
		parts = parts[:4]
	}
	return strings.Join(parts, "-")
}
