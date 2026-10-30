# Version detection

For each application, LUM compares two versions:
- the **installed version**, read according to `current`;
- the **latest available version**, read according to `upstream`.

From them it derives a state. The configuration fields are described in [configuration.md](configuration.md).

> Detection is used by scheduled and manual checks (see [checks-notifications.md](checks-notifications.md)).

## Latest available version (GitHub)

The release that is picked depends on `include_prerelease`:

| Situation | Request | Release picked |
|---|---|---|
| `include_prerelease: false` (default) | `GET /repos/{repo}/releases/latest` | the latest stable release according to GitHub |
| `include_prerelease: true` | `GET /repos/{repo}/releases?per_page=30` | the most recent one, excluding drafts, whose tag matches `tag_regex` |
| The repository publishes no releases | `GET /repos/{repo}/tags?per_page=100` | the **highest version** among the tags that match `tag_regex`; pre-releases are excluded unless `include_prerelease` is set |

**Tag normalization**: `tag_regex` extracts the canonical version through its named group `version`. For example, `v4.0.10.2544` gives `4.0.10.2544`. A tag that does not match produces an explicit error naming the tag.

**Information collected** for each release: version, tag, title, link to the release notes, markdown notes and publication date. A version that comes from a plain tag has neither notes nor date.

### Cache and rate limit

- **During `github.cache_ttl`** (15 min by default), a response already obtained is reused **without any request**.
- **After that**, the request is conditional (`If-None-Match` with the ETag). A `304 Not Modified` response reuses the cache and **does not consume the rate limit**.
- **Rate limit**: without `github.token`, GitHub allows 60 requests/hour; with one, 5000. The remaining quota is read from every response (`X-RateLimit-*`) and shown on the System status page.

### GitHub errors

| Case | Message |
|---|---|
| Repository does not exist | `repository not found: owner/name` |
| No usable tag | `no release or tag matches tag_regex: …` |
| 401 | `authentication failed (401), check github.token` |
| Rate limit exhausted | `rate limit exceeded, resets at …` |
| Other status, timeout (15 s) | message with the API path involved |

## Installed version

| `current.type` | How it works |
|---|---|
| `api` | `GET url` with the header `auth_header: token`, then extraction with `json_path` (e.g. `$.version`, `$.response.data.version`). A numeric value is accepted. Timeout: 10 s. |
| `agent` | The agent in the container runs `command`, and `output_regex` extracts the version from its output (see [agent.md](agent.md)). If the agent is not connected, the installed version is unknown and the state is `unknown`. |

Possible errors with the `api` type: unexpected HTTP status (with a hint for 401/403), non-JSON response, a `json_path` that matches nothing or does not point to a simple value, timeout.

### Secrets in URLs

Some applications require their API key in the URL (`?apikey=…`). In that case, put the key in a variable: `url: "http://192.0.2.30:8181/api/v2?cmd=get_info&apikey=${MYAPP_API_KEY}"`.

Error messages never contain these secrets: URLs are cleaned before being displayed.
- Values of query parameters whose name contains `key`, `token`, `secret`, `pass`, `auth`, `sig` or `credential` are replaced with `****`.
- A `user:pass@` password is masked the same way.
- This also applies to network errors, which include the full URL.

## Comparison and states

Versions are compared as extended SemVer:
- 2 to 4 numeric segments; a missing segment counts as 0, so `12.3` = `12.3.0` and `1.2.3` = `1.2.3.0`;
- numeric comparison, so `1.10.0` > `1.9.9`;
- a leading `v` is tolerated;
- a `-prerelease` suffix follows SemVer rules: `1.0.0-beta` < `1.0.0`, and `beta.2` < `beta.11`;
- `+build` metadata is ignored.

| State | Condition |
|---|---|
| `update_available` | installed < available |
| `up_to_date` | installed ≥ available (an installed version newer than the public release counts as up to date) |
| `unknown` | one of the two versions is missing or unreadable (e.g. agent not connected, `nightly`) |
| `error` | collection failed (GitHub or the application's API), with the error message |
