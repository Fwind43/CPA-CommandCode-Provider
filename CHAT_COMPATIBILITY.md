# Chat Completions compatibility status

This adapter is NOT a complete implementation of the OpenAI Chat Completions API.

## Locally verified

- Stable completion ID and creation time across streaming chunks.
- Initial assistant role delta and terminal finish delta.
- Optional final usage chunk controlled by stream_options.include_usage;
  preceding chunks carry null usage when requested.
- Reasoning deltas are separate from answer text (reasoning_content extension).
- Function tools and tool result history (existing regression tests).
- Ordered text and base64 image content mapping.
- Responses input_image with image_url is normalized to the same image path;
  missing/non-string URLs and file_id-only inputs fail explicitly.

## Live probe (2026-09-20)

One management api-call attempt with a generated blue-square/OCR image was
rejected with HTTP 400 `auth token not found` before model inference. The
management token substitution route did not resolve the selected plugin account.
No successful live vision inference has been verified; no deployment was made.

## Parameter mapping

- max_tokens, temperature (including zero), and reasoning_effort mapping follows
  the installed official CommandCode CLI 1.54.1 wire format.
- Explicit token limits are no longer silently capped at 32768.

Tests use an HTTP fixture and the host callback boundary, not live model inference.
The host, not the plugin, wraps JSON in SSE and supplies [DONE].

## Remaining gaps / unverified

- Required/named tool_choice and custom tools in the Chat path.
- response_format / strict structured output and parallel tool constraints.
- top_p, stop, penalties, seed, logprobs and related generation options.
- Multiple choices, audio, file inputs and remote image URLs.
- Complete OpenAI validation and HTTP error/status propagation through the host.
- Live enforcement of generation parameters and model-specific vision support.

Unsupported content parts now fail instead of silently disappearing. Remote
image URLs currently fail explicitly; this is a limitation, not full vision
compatibility. Other unmapped request fields can still be ignored by existing
normalization and need further work. Do not advertise complete compatibility or
release this as such before closing the gaps and verifying upstream behavior.
