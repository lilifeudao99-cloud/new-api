/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/**
 * Recovery for code-split chunks that can no longer be fetched.
 *
 * Every release replaces the hashed files under `/static/`. A tab that was
 * opened before the release still holds the old runtime, so navigating to a
 * route whose chunk was replaced fails with a chunk load error. Reloading the
 * page fetches the current `index.html` and resolves it. The reload is
 * throttled through sessionStorage so a persistent network failure can never
 * turn into a reload loop.
 */
const CHUNK_RELOAD_AT_KEY = 'app:chunk-reload-at'
const CHUNK_RELOAD_MIN_INTERVAL_MS = 60_000

const CHUNK_LOAD_ERROR_PATTERNS = [
  /Loading (CSS )?chunk [\w-]+ failed/i,
  /Failed to fetch dynamically imported module/i,
  /error loading dynamically imported module/i,
  /Importing a module script failed/i,
]

export function isChunkLoadError(error: unknown): boolean {
  if (typeof error !== 'object' || error === null) return false
  const { name, message } = error as { name?: unknown; message?: unknown }
  if (name === 'ChunkLoadError') return true
  if (typeof message !== 'string') return false
  return CHUNK_LOAD_ERROR_PATTERNS.some((pattern) => pattern.test(message))
}

/**
 * Reloads the page when `error` is a chunk load failure, at most once per
 * minute per tab. Returns true when a reload was started.
 */
export function reloadForChunkLoadError(error: unknown): boolean {
  if (typeof window === 'undefined' || !isChunkLoadError(error)) return false
  try {
    const lastReloadAt = Number(
      window.sessionStorage.getItem(CHUNK_RELOAD_AT_KEY) ?? 0
    )
    if (Date.now() - lastReloadAt < CHUNK_RELOAD_MIN_INTERVAL_MS) return false
    window.sessionStorage.setItem(CHUNK_RELOAD_AT_KEY, String(Date.now()))
  } catch {
    // Without storage the throttle cannot be enforced; never risk a loop.
    return false
  }
  window.location.reload()
  return true
}
