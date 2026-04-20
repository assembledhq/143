# Platform LLM (Self-Hosting)

143 uses a small, cheap LLM internally to power background features that aren't part of an agent session. On the hosted version at [143.dev](https://www.143.dev) this is already configured. If you're running your own instance, you need to provide a key for it.

If no platform LLM is configured, the in-app alert on **Settings → LLM** points here, and the affected features silently no-op.

## What it powers

The platform LLM runs background tasks that the user doesn't directly invoke — things like summarizing a session into a title, drafting PR descriptions, generating project drafts from raw input, scoring priority, and validating proposed work. These are intentionally cheap, latency-tolerant calls, which is why they run on a smaller model than the one you use for coding agent sessions.

This list is not load-bearing on this doc — for the authoritative set, search the codebase for `PlatformLLMConfig`.

## How to enable it

Set one of the following provider keys on the **backend** environment:

- `ANTHROPIC_API_KEY`
- `OPENAI_API_KEY`
- `OPENROUTER_API_KEY`

Then pick a model with `PLATFORM_LLM_MODEL`. The default is `gpt-5-nano`, which assumes an OpenAI-compatible key. If you've only set an Anthropic key, set `PLATFORM_LLM_MODEL` to a corresponding small Anthropic model (e.g. `claude-haiku-4-5-20251001`); same idea for OpenRouter.

The agent-session model (`LLM_MODEL`) is independent — the platform LLM does not use it.

### Minimal example (OpenAI)

```sh
OPENAI_API_KEY=sk-...
# PLATFORM_LLM_MODEL is gpt-5-nano by default; no need to set it
```

### Minimal example (Anthropic)

```sh
ANTHROPIC_API_KEY=sk-ant-...
PLATFORM_LLM_MODEL=claude-haiku-4-5-20251001
```

## Cost notes

The defaults are deliberately cheap. Background features fire often (every new session, every PR, every project draft), so picking a frontier model here is mostly waste — the latency hurts the UX more than the quality helps it. Stick with a `nano` / `haiku` / equivalently small model unless you have a specific reason not to.

## Verifying it works

After setting the env vars and restarting the backend, the alert on **Settings → LLM** disappears. To confirm end-to-end, start a new session and watch for the auto-generated title within a few seconds — that call goes through the platform LLM.
