package app

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"kogen-go/internal/contract"
	"kogen-go/internal/project"
	shapesession "kogen-go/internal/shape/session"
	"kogen-go/internal/yamlmini"
)

func TestReadShapeRequestPreservesRawBytes(t *testing.T) {
	want := []byte("line one\r\nline two\x00\n")
	requestPath := filepath.Join(t.TempDir(), "request.md")
	if err := os.WriteFile(requestPath, want, 0o600); err != nil {
		t.Fatal(err)
	}

	got, issue := readShapeRequest(bytes.NewReader(nil), filepath.Dir(requestPath), filepath.Base(requestPath))
	if issue != "" || !bytes.Equal(got, want) {
		t.Fatalf("file request = %q, %q; want exact bytes %q", got, issue, want)
	}

	stdin := []byte("stdin\r\nrequest\n")
	got, issue = readShapeRequest(bytes.NewReader(stdin), ".", "-")
	if issue != "" || !bytes.Equal(got, stdin) {
		t.Fatalf("stdin request = %q, %q; want exact bytes %q", got, issue, stdin)
	}
}

func TestReadShapeRequestReportsMissingAndEmptyInputs(t *testing.T) {
	root := t.TempDir()
	if got, issue := readShapeRequest(bytes.NewReader(nil), root, "missing.md"); got != nil || issue != "not found" {
		t.Fatalf("missing request = %q, %q", got, issue)
	}
	if got, issue := readShapeRequest(bytes.NewReader(nil), root, "-"); got != nil || issue != "empty" {
		t.Fatalf("empty stdin request = %q, %q", got, issue)
	}
	empty := filepath.Join(root, "empty.md")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, issue := readShapeRequest(bytes.NewReader(nil), root, empty); got != nil || issue != "empty" {
		t.Fatalf("empty file request = %q, %q", got, issue)
	}
}

func TestShapeConversationFactoryKeepsRunAffinityAndAliasesFallback(t *testing.T) {
	for _, provider := range []string{"chatgpt", "grok"} {
		t.Run(provider, func(t *testing.T) {
			manifest, err := project.ResolveRoles(provider, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			factory := shapeConversationFactory("run_shape_test", "cache_shape_test")
			primary, err := factory(shapesessionSpec(1, "shaper", "shaper", manifest.Effective[contract.RoleName("shaper")]))
			if err != nil {
				t.Fatal(err)
			}
			fallback, err := factory(shapesessionSpec(2, "fallback_shaper", "shaper", manifest.FallbackShaper))
			if err != nil {
				t.Fatal(err)
			}
			primaryID, fallbackID := primary.Identity(), fallback.Identity()
			if primaryID.RunID != "run_shape_test" || primaryID.CacheKey != "cache_shape_test" || primaryID.SessionID != "cache_shape_test" {
				t.Fatalf("primary identity lost run affinity: %#v", primaryID)
			}
			if fallbackID.RunID != primaryID.RunID || fallbackID.CacheKey != primaryID.CacheKey || fallbackID.SessionID != primaryID.SessionID || fallbackID.ThreadID == primaryID.ThreadID {
				t.Fatalf("fallback did not get a fresh thread on the same run affinity: primary=%#v fallback=%#v", primaryID, fallbackID)
			}
			if fallbackID.Role != "fallback_shaper" || fallbackID.Provider != manifest.Effective[contract.RoleName("shaper")].Provider ||
				fallbackID.Model != manifest.Effective[contract.RoleName("shaper")].Model || fallbackID.Effort != manifest.Effective[contract.RoleName("shaper")].Effort {
				t.Fatalf("fallback identity did not alias the resolved shaper tuple: %#v, manifest=%#v", fallbackID, manifest)
			}
		})
	}
}

func shapesessionSpec(index int, assigned, effective contract.RoleName, settings contract.RoleSettings) shapesession.ConversationSpec {
	return shapesession.ConversationSpec{Index: index, AssignedRole: assigned, EffectiveRole: effective, Settings: settings}
}

func TestSelectShapeAcceptanceUsesRailsAndExUnitFixtures(t *testing.T) {
	t.Run("Rails", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, filepath.Join(root, "Gemfile"), "source 'https://rubygems.org'\ngem 'rails'\n")
		writeFixture(t, filepath.Join(root, "config", "application.rb"), "module Fixture; end\n")
		resolved := &project.Resolution{Checkout: root}
		adapter, err := selectShapeAcceptance(resolved, filepath.Join(t.TempDir(), "run"), nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if adapter.kind != "rails" {
			t.Fatalf("selected adapter = %q, want Rails", adapter.kind)
		}
		paths, err := adapter.Paths("sample-shape")
		if err != nil || paths.Source != ".kogen/acceptance/sample-shape_test.rb" || paths.Candidate != "test/acceptance/sample-shape_test.rb" {
			t.Fatalf("Rails paths = %#v, %v", paths, err)
		}
		if !reflect.DeepEqual(adapter.config.Run, []string{"bundle", "exec", "rails", "test", "{path}"}) {
			t.Fatalf("Rails command = %#v", adapter.config.Run)
		}
		if adapter.env["RAILS_ENV"] != "test" || adapter.env["BUNDLE_PATH"] != filepath.Join(root, "vendor", "cache") {
			t.Fatalf("Rails child environment = %#v", adapter.env)
		}
	})

	t.Run("ExUnit", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, filepath.Join(root, "mix.exs"), "defmodule Fixture.MixProject do end\n")
		writeFixture(t, filepath.Join(root, ".tool-versions"), "elixir 1.17\nerlang 27\n")
		adapter, err := selectShapeAcceptance(&project.Resolution{Checkout: root}, filepath.Join(t.TempDir(), "run"), nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if adapter.kind != "exunit" || !adapter.useMise {
			t.Fatalf("ExUnit selection = kind %q, useMise %t", adapter.kind, adapter.useMise)
		}
		paths, err := adapter.Paths("sample-shape")
		if err != nil || paths.Source != ".kogen/acceptance/sample-shape_test.exs" || paths.Candidate != "test/acceptance/sample-shape_test.exs" {
			t.Fatalf("ExUnit paths = %#v, %v", paths, err)
		}
	})
}

func TestSelectShapeAcceptanceHonorsExplicitCommandConfig(t *testing.T) {
	root := t.TempDir()
	config := &project.Config{Raw: yamlmini.Mapping{
		"acceptance": yamlmini.Mapping{
			"adapter":       "command",
			"ext":           ".feature",
			"candidate_dir": "spec/features",
			"run":           yamlmini.Sequence{"feature-runner", "--test", "{path}"},
		},
	}}
	adapter, err := selectShapeAcceptance(&project.Resolution{Checkout: root, Config: config}, filepath.Join(t.TempDir(), "run"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := adapter.Paths("sample-shape")
	if err != nil || paths.Source != ".kogen/acceptance/sample-shape.feature" || paths.Candidate != "spec/features/sample-shape.feature" {
		t.Fatalf("configured command paths = %#v, %v", paths, err)
	}
	if !reflect.DeepEqual(adapter.config.Run, []string{"feature-runner", "--test", "{path}"}) {
		t.Fatalf("configured command = %#v", adapter.config.Run)
	}
}

func writeFixture(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
