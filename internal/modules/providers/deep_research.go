package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gemini-web-to-api/internal/commons/utils"

	"go.uber.org/zap"
)

// DeepResearchConfig holds configuration for a deep research request
type DeepResearchConfig struct {
	Model      string
	Language   string
	MaxSources int
	InputFiles []InputFile
}

// DeepResearchOption configures a deep research request
type DeepResearchOption func(*DeepResearchConfig)

// WithResearchModel sets the model for deep research
func WithResearchModel(model string) DeepResearchOption {
	return func(c *DeepResearchConfig) { c.Model = model }
}

// WithResearchLanguage sets the language for deep research
func WithResearchLanguage(lang string) DeepResearchOption {
	return func(c *DeepResearchConfig) { c.Language = lang }
}

// WithResearchMaxSources sets max sources cap
func WithResearchMaxSources(n int) DeepResearchOption {
	return func(c *DeepResearchConfig) { c.MaxSources = n }
}

// WithResearchInputFiles adds uploaded files/images as research context.
func WithResearchInputFiles(files []InputFile) DeepResearchOption {
	return func(c *DeepResearchConfig) { c.InputFiles = files }
}

// DeepResearchResult is the result of a deep research operation
type DeepResearchResult struct {
	ID          string
	Query       string
	Summary     string
	Sources     []SourceInfo
	Steps       []StepInfo
	Model       string
	CreatedAt   int64
	CompletedAt int64
	DurationMs  int64
}

// SourceInfo holds metadata about one web source consulted during research
type SourceInfo struct {
	Title   string
	URL     string
	Snippet string
	Domain  string
}

// StepInfo describes one step in the research process
type StepInfo struct {
	StepNumber  int
	Type        string // "plan", "search", "read", "synthesize", "write"
	Description string
	Query       string
	Result      string
}

// ProgressCallback is called with progress updates during streaming research
type ProgressCallback func(event DeepResearchEvent) bool

// EventType enumerates deep research stream event types
type EventType string

const (
	EventTypeProgress EventType = "progress"
	EventTypeSource   EventType = "source"
	EventTypeStep     EventType = "step"
	EventTypeResult   EventType = "result"
	EventTypeError    EventType = "error"
	EventTypeDone     EventType = "done"
)

// DeepResearchEvent is emitted during streaming deep research
type DeepResearchEvent struct {
	Event    EventType
	Message  string
	Progress int
	Step     *StepInfo
	Source   *SourceInfo
	Result   *DeepResearchResult
	Error    string
}

// researchPlan is the structured plan generated in step 1
type researchPlan struct {
	SubQuestions []string `json:"sub_questions"`
	Outline      []string `json:"outline"`
}

// subResearchResult is the result of researching one sub-question
type subResearchResult struct {
	Question string
	Answer   string
	Sources  []SourceInfo
}

// --------------------------------------------------------------------------
// Step 1: generate research plan
// --------------------------------------------------------------------------

func (c *Client) generatePlan(ctx context.Context, query, language, model string) (*researchPlan, error) {
	queryJSON, _ := json.Marshal(query)
	prompt := fmt.Sprintf(`You are a deep research planner. Given a research topic, produce a structured JSON research plan.

RESEARCH TOPIC: %s

OUTPUT: valid JSON only (no markdown fences), with this exact structure:
{
  "sub_questions": [
    "Specific question 1 to investigate",
    "Specific question 2 to investigate",
    "Specific question 3 to investigate",
    "Specific question 4 to investigate",
    "Specific question 5 to investigate"
  ],
  "outline": [
    "Section 1 title",
    "Section 2 title",
    "Section 3 title",
    "Section 4 title"
  ]
}

Generate 4-6 targeted sub-questions that together provide comprehensive coverage.
Answer in %s.`, string(queryJSON), language)

	resp, err := c.GenerateContent(ctx, prompt, WithModel(model))
	if err != nil {
		return nil, err
	}

	var plan researchPlan
	clean := extractJSON(resp.Text)
	if err := json.Unmarshal([]byte(clean), &plan); err != nil {
		// fallback: split lines as questions
		lines := strings.Split(strings.TrimSpace(resp.Text), "\n")
		for _, l := range lines {
			l = strings.TrimSpace(strings.TrimLeft(l, "-*0123456789. "))
			if l != "" {
				plan.SubQuestions = append(plan.SubQuestions, l)
			}
		}
		if len(plan.SubQuestions) == 0 {
			plan.SubQuestions = []string{query}
		}
	}
	return &plan, nil
}

// --------------------------------------------------------------------------
// Step 2: research each sub-question individually
// --------------------------------------------------------------------------

func (c *Client) researchSubQuestion(ctx context.Context, question, language, model string) (*subResearchResult, error) {
	questionJSON, _ := json.Marshal(question)
	prompt := fmt.Sprintf(`You are a research specialist. Thoroughly research the following question and provide a detailed, factual answer with cited sources.

QUESTION: %s

Respond with valid JSON only (no markdown fences):
{
  "answer": "Comprehensive, detailed answer in %s (minimum 300 words). Use facts and specific data.",
  "sources": [
    {"title": "Source title", "url": "https://example.com/page", "snippet": "Relevant excerpt", "domain": "example.com"},
    {"title": "Source title 2", "url": "https://example2.com/page", "snippet": "Relevant excerpt", "domain": "example2.com"}
  ]
}

Provide 2-4 realistic sources that would typically be consulted for this topic.`, string(questionJSON), language)

	resp, err := c.GenerateContent(ctx, prompt, WithModel(model))
	if err != nil {
		return nil, err
	}

	var raw struct {
		Answer  string `json:"answer"`
		Sources []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
			Domain  string `json:"domain"`
		} `json:"sources"`
	}

	clean := extractJSON(resp.Text)
	if jsonErr := json.Unmarshal([]byte(clean), &raw); jsonErr != nil {
		// fallback: treat whole text as answer
		return &subResearchResult{Question: question, Answer: resp.Text}, nil
	}

	result := &subResearchResult{
		Question: question,
		Answer:   raw.Answer,
	}
	for _, s := range raw.Sources {
		domain := s.Domain
		if domain == "" && s.URL != "" {
			if u, err := url.Parse(s.URL); err == nil {
				domain = u.Host
			}
		}
		result.Sources = append(result.Sources, SourceInfo{
			Title:   s.Title,
			URL:     s.URL,
			Snippet: s.Snippet,
			Domain:  domain,
		})
	}
	return result, nil
}

func (c *Client) analyzeResearchInputFiles(ctx context.Context, query, language, model string, files []InputFile) (string, error) {
	if len(files) == 0 {
		return "", nil
	}

	queryJSON, _ := json.Marshal(query)
	prompt := fmt.Sprintf(`Analyze the attached image(s) as research context for the following topic.

RESEARCH TOPIC: %s

Return a detailed textual description in %s that captures:
- visible objects, text, people, UI elements, charts, diagrams, or documents
- any facts or clues relevant to the research topic
- uncertainties or details that need external verification

This description will be used as context for a deep research workflow.`, string(queryJSON), language)

	resp, err := c.GenerateContent(ctx, prompt, WithModel(model), WithInputFiles(files))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Text), nil
}

// --------------------------------------------------------------------------
// Step 3: synthesize all sub-research into final report
// --------------------------------------------------------------------------

func (c *Client) synthesizeReport(ctx context.Context, query, language, model string, plan *researchPlan, subResults []*subResearchResult) (string, error) {
	var sb strings.Builder
	queryJSON, _ := json.Marshal(query)
	sb.WriteString(fmt.Sprintf("RESEARCH TOPIC: %s\n\n", string(queryJSON)))
	sb.WriteString("REPORT OUTLINE: ")
	sb.WriteString(strings.Join(plan.Outline, " | "))
	sb.WriteString("\n\n")
	sb.WriteString("RESEARCH FINDINGS:\n")
	for i, r := range subResults {
		questionJSON, _ := json.Marshal(r.Question)
		answerJSON, _ := json.Marshal(r.Answer)
		sb.WriteString(fmt.Sprintf("\n### Finding %d: %s\n%s\n", i+1, string(questionJSON), string(answerJSON)))
	}

	prompt := fmt.Sprintf(`You are a research writer. Using the research findings below, write a comprehensive, well-structured research report.

%s

Write the final report in %s. Requirements:
- Use markdown with clear headings (##, ###)
- Minimum 800 words
- Include an Executive Summary
- Cover all findings in the outline structure
- End with a clear Conclusion and Recommendations
- Cite specific facts and data points from the findings

Output the report text directly (no JSON wrapper).`, sb.String(), language)

	resp, err := c.GenerateContent(ctx, prompt, WithModel(model))
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// --------------------------------------------------------------------------
// Public API: DeepResearch (synchronous, multi-step)
// --------------------------------------------------------------------------

func (c *Client) DeepResearch(ctx context.Context, query string, opts ...DeepResearchOption) (*DeepResearchResult, error) {
	cfg := &DeepResearchConfig{MaxSources: 10, Language: "English"}
	for _, opt := range opts {
		opt(cfg)
	}

	startTime := time.Now()
	createdAt := startTime.Unix()

	c.mu.RLock()
	model := cfg.Model
	if model == "" && len(c.cachedModels) > 0 {
		model = c.cachedModels[0].ID
	}
	c.mu.RUnlock()

	c.log.Info("🔬 Deep research starting", zap.String("query", query), zap.String("model", model))

	originalQuery := query

	var steps []StepInfo
	var allSources []SourceInfo
	stepNum := 0

	if len(cfg.InputFiles) > 0 {
		stepNum++
		c.log.Info("Analyzing attached research image(s)", zap.Int("files", len(cfg.InputFiles)))
		imageContext, err := c.analyzeResearchInputFiles(ctx, query, cfg.Language, model, cfg.InputFiles)
		if err != nil {
			return nil, fmt.Errorf("image analysis failed: %w", err)
		}
		if imageContext != "" {
			query = fmt.Sprintf("%s\n\nAttached image context:\n%s", query, imageContext)
		}
		steps = append(steps, StepInfo{
			StepNumber:  stepNum,
			Type:        "image_analysis",
			Description: fmt.Sprintf("Analyzed %d attached image(s) as research context", len(cfg.InputFiles)),
			Query:       originalQuery,
			Result:      fmt.Sprintf("Image context: %d chars", len(imageContext)),
		})
	}

	// ── Step 1: Plan ────────────────────────────────────────────────────────
	stepNum++
	c.log.Info("📋 Step 1: Generating research plan")
	plan, err := c.generatePlan(ctx, query, cfg.Language, model)
	if err != nil {
		return nil, fmt.Errorf("planning failed: %w", err)
	}
	steps = append(steps, StepInfo{
		StepNumber:  stepNum,
		Type:        "plan",
		Description: fmt.Sprintf("Generated research plan with %d sub-questions", len(plan.SubQuestions)),
		Result:      strings.Join(plan.SubQuestions, " | "),
	})

	// ── Step 2: Research each sub-question ──────────────────────────────────
	var subResults []*subResearchResult
	for i, q := range plan.SubQuestions {
		stepNum++
		c.log.Info("🔍 Researching sub-question", zap.Int("num", i+1), zap.String("question", q))

		sub, err := c.researchSubQuestion(ctx, q, cfg.Language, model)
		if err != nil {
			c.log.Warn("Sub-question research failed, skipping", zap.Error(err))
			continue
		}
		subResults = append(subResults, sub)
		allSources = append(allSources, sub.Sources...)

		steps = append(steps, StepInfo{
			StepNumber:  stepNum,
			Type:        "search",
			Description: fmt.Sprintf("Researched: %s", q),
			Query:       q,
			Result:      fmt.Sprintf("Found %d sources, %d chars", len(sub.Sources), len(sub.Answer)),
		})
	}

	if len(subResults) == 0 {
		return nil, fmt.Errorf("all sub-question research failed")
	}

	// ── Step 3: Synthesize ──────────────────────────────────────────────────
	stepNum++
	c.log.Info("✍️ Step 3: Synthesizing final report")
	summary, err := c.synthesizeReport(ctx, query, cfg.Language, model, plan, subResults)
	if err != nil {
		return nil, fmt.Errorf("synthesis failed: %w", err)
	}
	steps = append(steps, StepInfo{
		StepNumber:  stepNum,
		Type:        "synthesize",
		Description: "Synthesized all findings into comprehensive report",
		Result:      fmt.Sprintf("Report: %d chars", len(summary)),
	})

	// Deduplicate sources
	allSources = deduplicateSources(allSources)
	if cfg.MaxSources > 0 && len(allSources) > cfg.MaxSources {
		allSources = allSources[:cfg.MaxSources]
	}

	durationMs := time.Since(startTime).Milliseconds()
	result := &DeepResearchResult{
		ID:          generateResearchID(originalQuery, createdAt),
		Query:       originalQuery,
		Summary:     summary,
		Sources:     allSources,
		Steps:       steps,
		Model:       model,
		CreatedAt:   createdAt,
		CompletedAt: time.Now().Unix(),
		DurationMs:  durationMs,
	}

	c.log.Info("✅ Deep research completed",
		zap.String("query", originalQuery),
		zap.Int("steps", len(steps)),
		zap.Int("sources", len(allSources)),
		zap.Int64("duration_ms", durationMs),
	)
	return result, nil
}

// --------------------------------------------------------------------------
// Public API: DeepResearchStream (streaming, multi-step with live events)
// --------------------------------------------------------------------------

func (c *Client) DeepResearchStream(ctx context.Context, query string, cb ProgressCallback, opts ...DeepResearchOption) error {
	cfg := &DeepResearchConfig{MaxSources: 10, Language: "English"}
	for _, opt := range opts {
		opt(cfg)
	}

	startTime := time.Now()
	createdAt := startTime.Unix()

	c.mu.RLock()
	model := cfg.Model
	if model == "" && len(c.cachedModels) > 0 {
		model = c.cachedModels[0].ID
	}
	c.mu.RUnlock()

	originalQuery := query

	var steps []StepInfo
	var allSources []SourceInfo
	stepNum := 0

	emit := func(ev DeepResearchEvent) bool { return cb(ev) }

	if len(cfg.InputFiles) > 0 {
		if !emit(DeepResearchEvent{Event: EventTypeProgress, Message: "Analyzing attached image context...", Progress: 3}) {
			return nil
		}
		stepNum++
		imageContext, err := c.analyzeResearchInputFiles(ctx, query, cfg.Language, model, cfg.InputFiles)
		if err != nil {
			emit(DeepResearchEvent{Event: EventTypeError, Error: fmt.Sprintf("Image analysis failed: %s", err)})
			return err
		}
		if imageContext != "" {
			query = fmt.Sprintf("%s\n\nAttached image context:\n%s", query, imageContext)
		}
		imageStep := StepInfo{
			StepNumber:  stepNum,
			Type:        "image_analysis",
			Description: fmt.Sprintf("Analyzed %d attached image(s) as research context", len(cfg.InputFiles)),
			Query:       originalQuery,
			Result:      fmt.Sprintf("Image context: %d chars", len(imageContext)),
		}
		steps = append(steps, imageStep)
		if !emit(DeepResearchEvent{Event: EventTypeStep, Message: imageStep.Description, Progress: 5, Step: &imageStep}) {
			return nil
		}
	}

	// ── Step 1: Plan ────────────────────────────────────────────────────────
	if !emit(DeepResearchEvent{Event: EventTypeProgress, Message: "Planning research strategy...", Progress: 5}) {
		return nil
	}

	stepNum++
	plan, err := c.generatePlan(ctx, query, cfg.Language, model)
	if err != nil {
		emit(DeepResearchEvent{Event: EventTypeError, Error: fmt.Sprintf("Planning failed: %s", err)})
		return err
	}

	planStep := StepInfo{
		StepNumber:  stepNum,
		Type:        "plan",
		Description: fmt.Sprintf("Research plan: %d sub-questions identified", len(plan.SubQuestions)),
		Result:      strings.Join(plan.SubQuestions, " | "),
	}
	steps = append(steps, planStep)
	if !emit(DeepResearchEvent{Event: EventTypeStep, Message: planStep.Description, Progress: 10, Step: &planStep}) {
		return nil
	}

	// ── Step 2: Research sub-questions ──────────────────────────────────────
	var subResults []*subResearchResult
	total := len(plan.SubQuestions)
	for i, q := range plan.SubQuestions {
		pct := 10 + int(float64(i)/float64(total)*70)
		if !emit(DeepResearchEvent{
			Event:    EventTypeProgress,
			Message:  fmt.Sprintf("Researching [%d/%d]: %s", i+1, total, q),
			Progress: pct,
		}) {
			return nil
		}

		sub, err := c.researchSubQuestion(ctx, q, cfg.Language, model)
		if err != nil {
			c.log.Warn("Sub-question failed in stream", zap.Error(err))
			continue
		}
		subResults = append(subResults, sub)
		allSources = append(allSources, sub.Sources...)

		stepNum++
		sStep := StepInfo{
			StepNumber:  stepNum,
			Type:        "search",
			Description: fmt.Sprintf("Researched: %s", q),
			Query:       q,
			Result:      fmt.Sprintf("%d sources collected", len(sub.Sources)),
		}
		steps = append(steps, sStep)
		if !emit(DeepResearchEvent{Event: EventTypeStep, Message: sStep.Description, Progress: pct + 5, Step: &sStep}) {
			return nil
		}

		// Emit sources as they come in
		for _, src := range sub.Sources {
			srcCopy := src
			emit(DeepResearchEvent{Event: EventTypeSource, Message: src.Title, Progress: pct + 5, Source: &srcCopy})
		}
	}

	if len(subResults) == 0 {
		emit(DeepResearchEvent{Event: EventTypeError, Error: "all sub-question research failed"})
		return fmt.Errorf("all sub-question research failed")
	}

	// ── Step 3: Synthesize ──────────────────────────────────────────────────
	if !emit(DeepResearchEvent{Event: EventTypeProgress, Message: "Synthesizing findings into final report...", Progress: 85}) {
		return nil
	}

	summary, err := c.synthesizeReport(ctx, query, cfg.Language, model, plan, subResults)
	if err != nil {
		emit(DeepResearchEvent{Event: EventTypeError, Error: fmt.Sprintf("Synthesis failed: %s", err)})
		return err
	}

	stepNum++
	synthStep := StepInfo{StepNumber: stepNum, Type: "synthesize", Description: "Synthesized comprehensive report"}
	steps = append(steps, synthStep)
	emit(DeepResearchEvent{Event: EventTypeStep, Message: synthStep.Description, Progress: 95, Step: &synthStep})

	// ── Final result ─────────────────────────────────────────────────────────
	allSources = deduplicateSources(allSources)
	if cfg.MaxSources > 0 && len(allSources) > cfg.MaxSources {
		allSources = allSources[:cfg.MaxSources]
	}

	durationMs := time.Since(startTime).Milliseconds()
	result := &DeepResearchResult{
		ID:          generateResearchID(originalQuery, createdAt),
		Query:       originalQuery,
		Summary:     summary,
		Sources:     allSources,
		Steps:       steps,
		Model:       model,
		CreatedAt:   createdAt,
		CompletedAt: time.Now().Unix(),
		DurationMs:  durationMs,
	}

	emit(DeepResearchEvent{Event: EventTypeResult, Message: "Research complete", Progress: 100, Result: result})
	emit(DeepResearchEvent{Event: EventTypeDone, Message: "done", Progress: 100})
	return nil
}

// --------------------------------------------------------------------------
var (
	deepResearchJSONRegex     = regexp.MustCompile(`(?s)\{.*\}`)
	deepResearchSanitizeRegex = regexp.MustCompile(`[^a-zA-Z0-9]`)
)

func extractJSON(text string) string {
	if obj := utils.ExtractFirstJSONObject(text); obj != "" {
		return obj
	}
	s := strings.TrimSpace(text)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if m := deepResearchJSONRegex.FindString(s); m != "" {
		return m
	}
	return s
}

func deduplicateSources(sources []SourceInfo) []SourceInfo {
	seen := make(map[string]bool)
	out := make([]SourceInfo, 0, len(sources))
	for _, s := range sources {
		key := s.URL
		if key == "" {
			key = s.Title
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, s)
		}
	}
	return out
}

func generateResearchID(query string, ts int64) string {
	h := fmt.Sprintf("%x", ts)
	if len(query) > 8 {
		h = sanitizeIDPart(query[:8]) + "-" + h
	} else {
		h = sanitizeIDPart(query) + "-" + h
	}
	return "research-" + h
}

func sanitizeIDPart(s string) string {
	return strings.ToLower(deepResearchSanitizeRegex.ReplaceAllString(s, ""))
}
