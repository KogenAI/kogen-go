package streamsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"kogen-go/internal/contract"
	"kogen-go/internal/optional/checkpoint"
	"kogen-go/internal/provider/session"
	"kogen-go/internal/provider/wire"
	"kogen-go/internal/xspec/protocol"
)

type sessionObservation struct {
	Version         string            `json:"version"`
	Stage           string            `json:"stage"`
	Attempt         string            `json:"attempt"`
	Rung            string            `json:"rung"`
	Epoch           string            `json:"epoch"`
	EpochClass      string            `json:"epochClass"`
	Model           string            `json:"model"`
	RunName         string            `json:"runName"`
	AffinityChanged bool              `json:"affinityChanged"`
	Previous        bool              `json:"previous"`
	KeyChanged      bool              `json:"keyChanged"`
	Lite            string            `json:"lite"`
	Last            string            `json:"last"`
	SharedAffinity  bool              `json:"sharedAffinity"`
	Prefixes        map[string]string `json:"prefixes"`
}

type bindInput struct {
	Stage   string `json:"stage"`
	Attempt string `json:"attempt"`
	Rung    string `json:"rung"`
}

type nameInput struct {
	Name string `json:"name"`
}

type acceptInput struct {
	OK bool `json:"ok"`
}

type affinityInput struct {
	Shared bool `json:"shared"`
}

type prefixInput struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Adapter  string `json:"adapter"`
	Prompt   string `json:"prompt"`
	Bytes    string `json:"bytes"`
}

type prefixNamespace struct {
	provider string
	model    string
	adapter  string
	prompt   string
}

type sessionSlice struct {
	obs              sessionObservation
	runID            string
	cacheKey         string
	conversation     *session.Conversation
	prefixRegistries map[prefixNamespace]*wire.PrefixRegistry
}

func newSessionSlice() *sessionSlice {
	runName := "run-1"
	return &sessionSlice{
		obs:              sessionObservation{RunName: runName, Last: "ok", Prefixes: map[string]string{}},
		runID:            fixtureIdentity("run", runName),
		cacheKey:         fixtureIdentity("cache", runName),
		prefixRegistries: map[prefixNamespace]*wire.PrefixRegistry{},
	}
}

func (s *sessionSlice) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	*s = *newSessionSlice()
	return nil
}

func (s *sessionSlice) HasEventTag(tag string) bool {
	switch tag {
	case "Init", "Bind", "Turn", "Repair", "Model", "Stage", "Attempt", "Rung", "Epoch", "Accept", "Previous", "Lite", "NewRun", "AffinityScope", "Prefix":
		return true
	default:
		return false
	}
}

func (s *sessionSlice) Apply(ctx context.Context, event protocol.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch event.Tag {
	case "Init":
		return s.Reset(ctx)
	case "Bind":
		return s.bind(event)
	case "Turn":
		return s.touch(false)
	case "Repair":
		return s.touch(true)
	case "Model":
		return s.setModel(event)
	case "Stage":
		return s.setName(event, "stage")
	case "Attempt":
		return s.setName(event, "attempt")
	case "Rung":
		return s.setName(event, "rung")
	case "Epoch":
		return s.setEpoch(event)
	case "Accept":
		return s.accept(event)
	case "Previous":
		s.obs.Last = "never_sent"
		return nil
	case "Lite":
		return s.setLite()
	case "NewRun":
		return s.newRun(event)
	case "AffinityScope":
		return s.setAffinityScope(event)
	case "Prefix":
		return s.registerPrefix(event)
	default:
		return fmt.Errorf("streamsession: unsupported session event %q", event.Tag)
	}
}

func (s *sessionSlice) Observe(ctx context.Context) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(s.obs)
}

func (s *sessionSlice) bind(event protocol.Event) error {
	var input bindInput
	if !event.HasValue || json.Unmarshal(event.Value, &input) != nil || !knownStage(input.Stage) ||
		!knownAttempt(input.Attempt) || !knownRung(input.Rung) {
		s.obs.Last = "bad_bind"
		return nil
	}
	attempt := input.Attempt
	if attempt == "" {
		attempt = "builder"
	}
	rung := input.Rung
	if rung == "" {
		rung = attempt
	}
	wasBound := s.isBound()
	var oldThread string
	if wasBound {
		oldThread = s.conversation.Identity().ThreadID
	}
	if err := s.bindIdentity(input.Stage, attempt, rung, "initial", false); err != nil {
		return err
	}
	s.obs.KeyChanged = wasBound && s.conversation.Identity().ThreadID != oldThread
	s.obs.AffinityChanged = false
	s.obs.Last = "ok"
	return nil
}

func (s *sessionSlice) bindIdentity(stage, attempt, rung, epoch string, copyHistory bool) error {
	var previousThread string
	if s.conversation != nil {
		previousThread = s.conversation.Identity().ThreadID
	}
	modelName := s.obs.Model
	if modelName == "" {
		modelName = "luna"
	}
	settings := roleSettings(modelName)
	role := contract.RoleName("builder")
	if stage == "plan" {
		role = "planner"
	}
	runID, cacheKey, err := s.ensureRunIdentity()
	if err != nil {
		return err
	}
	identity, err := session.Bind(session.Binding{
		RunID: runID, CacheKey: cacheKey, Role: role,
		Provider: settings.Provider, Model: settings.Model, Effort: settings.Effort,
		Stage: stage, Attempt: attempt, Rung: rung, Epoch: epoch,
	})
	if err != nil {
		return err
	}
	if s.conversation == nil || previousThread != identity.ThreadID {
		var conversation *session.Conversation
		if copyHistory && s.conversation != nil && role == "builder" {
			turn, prepareErr := checkpoint.PrepareSummarizer(s.conversation, 1, wire.DefaultPrefix())
			if prepareErr == nil && turn.Conversation.Identity().ThreadID == identity.ThreadID {
				conversation = turn.Conversation
			}
		}
		if conversation == nil {
			conversation, err = session.New(identity)
			if err != nil {
				return err
			}
			if copyHistory && s.conversation != nil {
				for _, item := range s.conversation.ProtocolSession().History {
					if err := conversation.Append(item.Kind, item.Raw); err != nil {
						return err
					}
				}
			}
		}
		s.conversation = conversation
	}
	s.obs.Version = "v2"
	s.obs.Stage = stage
	s.obs.Attempt = attempt
	s.obs.Rung = rung
	s.obs.Epoch = epoch
	s.obs.EpochClass = epochClass(epoch)
	s.obs.KeyChanged = previousThread != "" && previousThread != identity.ThreadID
	s.obs.AffinityChanged = false
	return nil
}

func (s *sessionSlice) setName(event protocol.Event, field string) error {
	if !s.isBound() {
		s.obs.Last = "not_bound"
		return nil
	}
	var input nameInput
	if !event.HasValue || json.Unmarshal(event.Value, &input) != nil {
		s.obs.Last = "bad_bind"
		return nil
	}
	switch field {
	case "stage":
		if !knownStage(input.Name) {
			s.obs.Last = "bad_bind"
			return nil
		}
		if err := s.bindIdentity(input.Name, s.obs.Attempt, s.obs.Rung, s.obs.Epoch, false); err != nil {
			return err
		}
	case "attempt":
		if !knownRealAttempt(input.Name) {
			s.obs.Last = "bad_bind"
			return nil
		}
		if err := s.bindIdentity(s.obs.Stage, input.Name, s.obs.Rung, s.obs.Epoch, false); err != nil {
			return err
		}
	case "rung":
		if !knownRealRung(input.Name) {
			s.obs.Last = "bad_bind"
			return nil
		}
		if err := s.bindIdentity(s.obs.Stage, s.obs.Attempt, input.Name, s.obs.Epoch, false); err != nil {
			return err
		}
	}
	s.obs.Last = "ok"
	return nil
}

func (s *sessionSlice) setModel(event protocol.Event) error {
	if !s.isBound() {
		s.obs.Last = "not_bound"
		return nil
	}
	var input nameInput
	if !event.HasValue || json.Unmarshal(event.Value, &input) != nil || (input.Name != "luna" && input.Name != "sol") {
		s.obs.Last = "bad_model"
		return nil
	}
	settings := roleSettings(input.Name)
	if err := s.conversation.SwitchModel(settings.Model, settings.Effort); err != nil {
		return err
	}
	s.obs.Model = input.Name
	s.obs.KeyChanged = false
	s.obs.AffinityChanged = false
	s.obs.Last = "ok"
	return nil
}

func (s *sessionSlice) setEpoch(event protocol.Event) error {
	if !s.isBound() {
		s.obs.Last = "not_bound"
		return nil
	}
	var input nameInput
	if !event.HasValue || json.Unmarshal(event.Value, &input) != nil {
		s.obs.Last = "bad_epoch"
		return nil
	}
	target := ""
	switch input.Name {
	case "mutation-advice":
		target = "mutation-advice"
	case "summarizer":
		target = "checkpoint-1"
	default:
		s.obs.Last = "bad_epoch"
		return nil
	}
	if err := s.bindIdentity(s.obs.Stage, s.obs.Attempt, s.obs.Rung, target, input.Name == "summarizer"); err != nil {
		return err
	}
	s.obs.Last = "ok"
	return nil
}

func (s *sessionSlice) accept(event protocol.Event) error {
	if !s.isBound() {
		s.obs.Last = "not_bound"
		return nil
	}
	var input acceptInput
	if !event.HasValue || json.Unmarshal(event.Value, &input) != nil || !input.OK {
		s.obs.Last = "no_epoch"
		return nil
	}
	checkpointValue, err := checkpoint.BuildCheckpoint("session replay continuation", 4096)
	if err != nil {
		return err
	}
	oldThread := s.conversation.Identity().ThreadID
	if s.conversation.Identity().Role == "builder" {
		turn, continuationErr := checkpoint.NewContinuation(
			s.conversation.Identity(), fixtureApprovedInputs(), checkpointValue, wire.DefaultPrefix(),
		)
		if continuationErr != nil {
			return continuationErr
		}
		s.conversation = turn.Conversation
	} else {
		if err := s.bindIdentity(s.obs.Stage, s.obs.Attempt, s.obs.Rung, checkpointValue.Epoch(), false); err != nil {
			return err
		}
	}
	s.obs.KeyChanged = oldThread != s.conversation.Identity().ThreadID
	s.obs.Epoch = "digest"
	s.obs.EpochClass = "checkpoint"
	s.obs.AffinityChanged = false
	s.obs.Last = "ok"
	return nil
}

func (s *sessionSlice) touch(repair bool) error {
	if !s.isBound() {
		s.obs.Last = "not_bound"
		return nil
	}
	var err error
	if repair {
		err = s.conversation.AppendControllerMessage("session replay repair feedback")
	} else {
		err = s.conversation.AppendUser("session replay turn")
	}
	if err != nil {
		return err
	}
	s.obs.KeyChanged = false
	s.obs.AffinityChanged = false
	s.obs.Last = "ok"
	return nil
}

func (s *sessionSlice) setLite() error {
	if !s.isBound() {
		s.obs.Last = "not_bound"
		return nil
	}
	identity := s.conversation.Identity()
	if identity.SessionID == "" || identity.SessionID == identity.CacheKey ||
		identity.SessionID != session.DeriveLiteSessionID(identity.CacheKey) {
		return errors.New("streamsession: production session returned an invalid Lite identity")
	}
	s.obs.Lite = "v1"
	s.obs.KeyChanged = false
	s.obs.AffinityChanged = false
	s.obs.Last = "ok"
	return nil
}

func (s *sessionSlice) newRun(event protocol.Event) error {
	var input nameInput
	if !event.HasValue || json.Unmarshal(event.Value, &input) != nil || input.Name == "" || input.Name == s.obs.RunName {
		s.obs.Last = "bad_run"
		return nil
	}
	oldCacheKey := s.cacheKey
	s.obs.RunName = input.Name
	s.runID = fixtureIdentity("run", input.Name)
	if !s.obs.SharedAffinity {
		s.cacheKey = fixtureIdentity("cache", input.Name)
	}
	if s.cacheKey == "" {
		s.cacheKey = fixtureIdentity("cache", "shared-affinity")
	}
	s.conversation = nil
	s.obs = sessionObservation{
		RunName: input.Name, Last: "ok", Prefixes: s.obs.Prefixes,
		SharedAffinity: s.obs.SharedAffinity, AffinityChanged: oldCacheKey != s.cacheKey,
	}
	return nil
}

func (s *sessionSlice) setAffinityScope(event protocol.Event) error {
	if s.isBound() {
		s.obs.Last = "already_bound"
		return nil
	}
	var input affinityInput
	if !event.HasValue || json.Unmarshal(event.Value, &input) != nil {
		s.obs.Last = "bad_event"
		return nil
	}
	s.obs.SharedAffinity = input.Shared
	s.obs.AffinityChanged = false
	s.obs.Last = "ok"
	return nil
}

func (s *sessionSlice) registerPrefix(event protocol.Event) error {
	var input prefixInput
	if !event.HasValue || json.Unmarshal(event.Value, &input) != nil || input.Provider == "" || input.Model == "" ||
		input.Adapter == "" || input.Prompt == "" || strings.TrimSpace(input.Bytes) == "" {
		s.obs.Last = "bad_prefix"
		return nil
	}
	namespace := prefixNamespace{provider: input.Provider, model: input.Model, adapter: input.Adapter, prompt: input.Prompt}
	version := wire.PrefixVersion{Adapter: input.Adapter, Prompt: input.Prompt, Tools: "tools-v1"}
	prefix, err := wire.NewPrefix(version, input.Bytes, wire.CanonicalToolSchemas())
	if err != nil {
		s.obs.Last = "bad_prefix"
		return nil
	}
	registry := s.prefixRegistries[namespace]
	if registry == nil {
		registry = &wire.PrefixRegistry{}
		s.prefixRegistries[namespace] = registry
	}
	if err := registry.Register(input.Provider, prefix); err != nil {
		s.obs.Last = "static_prefix_changed"
		return nil
	}
	s.obs.Prefixes[pythonTupleKey(input.Provider, input.Model, input.Adapter, input.Prompt)] = input.Bytes
	s.obs.Last = "ok"
	return nil
}

func (s *sessionSlice) ensureRunIdentity() (string, string, error) {
	if s.runID == "" {
		s.runID = fixtureIdentity("run", s.obs.RunName)
	}
	if s.cacheKey == "" {
		cacheName := s.obs.RunName
		if s.obs.SharedAffinity {
			cacheName = "shared-affinity"
		}
		s.cacheKey = fixtureIdentity("cache", cacheName)
	}
	return s.runID, s.cacheKey, nil
}

func (s *sessionSlice) isBound() bool {
	return s.obs.Version == "v2" && s.conversation != nil
}

func fixtureIdentity(prefix, name string) string {
	digest := sha256.Sum256([]byte("kogen:xspec:" + prefix + ":" + name))
	return prefix + "_" + hex.EncodeToString(digest[:])
}

func fixtureApprovedInputs() checkpoint.ApprovedInputs {
	return checkpoint.ApprovedInputs{
		Request: json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"approved fixture request"}]}`),
		Plan:    json.RawMessage(`{"role":"user","content":[{"type":"input_text","text":"approved fixture plan"}]}`),
	}
}

func knownStage(value string) bool { return value == "develop" || value == "plan" }

func knownAttempt(value string) bool {
	return value == "" || value == "builder" || value == "fresh-1" || value == "fresh-2" || value == "escalation"
}

func knownRealAttempt(value string) bool {
	return value == "builder" || value == "fresh-1" || value == "fresh-2" || value == "escalation"
}

func knownRung(value string) bool {
	return value == "" || value == "builder" || value == "1" || value == "2" || value == "fresh-1"
}

func knownRealRung(value string) bool {
	return value == "builder" || value == "1" || value == "2" || value == "fresh-1"
}

func epochClass(epoch string) string {
	switch epoch {
	case "initial":
		return "initial"
	case "mutation-advice":
		return "mutation-advice"
	default:
		return "checkpoint"
	}
}

func pythonTupleKey(fields ...string) string {
	parts := make([]string, len(fields))
	for i, field := range fields {
		parts[i] = pythonRepr(field)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func pythonRepr(value string) string {
	quote := "'"
	if strings.Contains(value, "'") && !strings.Contains(value, `"`) {
		quote = `"`
	}
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, quote, `\`+quote)
	escaped = strings.ReplaceAll(escaped, "\n", `\n`)
	escaped = strings.ReplaceAll(escaped, "\r", `\r`)
	escaped = strings.ReplaceAll(escaped, "\t", `\t`)
	return quote + escaped + quote
}
