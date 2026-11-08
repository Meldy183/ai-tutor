export class VoiceApiError extends Error {
  constructor(
    operation: string,
    readonly status: number,
    readonly code = '',
  ) {
    super(`${operation} failed: ${status}`)
    this.name = 'VoiceApiError'
  }
}

export class StoredAudioError extends Error {
  constructor(
    readonly kind: 'http' | 'invalid' | 'network',
    readonly status = 0,
    readonly code = '',
  ) {
    super(
      kind === 'http'
        ? `Stored audio failed: ${status}`
        : `Stored audio ${kind}`,
    )
    this.name = 'StoredAudioError'
  }
}
import { apiFetch } from './api-fetch'
const apiBase = (import.meta.env.VITE_API_BASE_URL || '/api/v1').replace(
  /\/$/,
  '',
)

function requestSignal(signal: AbortSignal, timeoutMs = 120_000) {
  return AbortSignal.any([signal, AbortSignal.timeout(timeoutMs)])
}

function isAbortOrTimeout(error: unknown) {
  return (
    error instanceof DOMException &&
    (error.name === 'AbortError' || error.name === 'TimeoutError')
  )
}

export async function transcribeRecording(
  blob: Blob,
  signal: AbortSignal,
): Promise<{ id: string; text: string }> {
  const extension = blob.type.includes('ogg')
    ? 'ogg'
    : blob.type.includes('wav')
      ? 'wav'
      : blob.type.includes('mp4')
        ? 'm4a'
        : 'webm'
  const form = new FormData()
  form.append('audio', blob, `answer.${extension}`)
  const response = await apiFetch(`${apiBase}/voice/transcriptions`, {
    method: 'POST',
    credentials: 'include',
    body: form,
    // Backend allows 400 s, including the gateway queue and recognition.
    signal: requestSignal(signal, 430_000),
  })
  if (!response.ok) {
    const error: unknown = await response.json().catch(() => null)
    const code =
      error &&
      typeof error === 'object' &&
      'code' in error &&
      typeof error.code === 'string'
        ? error.code
        : ''
    throw new VoiceApiError('Transcription', response.status, code)
  }
  const data: unknown = await response.json()
  if (
    !data ||
    typeof data !== 'object' ||
    !('id' in data) ||
    typeof data.id !== 'string' ||
    !data.id ||
    !('text' in data) ||
    typeof data.text !== 'string' ||
    !data.text.trim()
  ) {
    throw new Error('Transcription is empty')
  }
  return { id: data.id, text: data.text.trim() }
}

export async function fetchStoredAudio(
  audioUrl: string,
  signal: AbortSignal,
): Promise<Blob> {
  if (!/^\/(?:learn\/)?task-audio\/[A-Za-z0-9_-]{1,256}\/file$/.test(audioUrl))
    throw new StoredAudioError('invalid')
  let response: Response
  const fetchSignal = requestSignal(signal)
  try {
    response = await apiFetch(`${apiBase}${audioUrl}`, {
      cache: 'no-store',
      credentials: 'include',
      signal: fetchSignal,
    })
  } catch (error) {
    fetchSignal.throwIfAborted()
    if (error instanceof TypeError) throw new StoredAudioError('network')
    throw error
  }
  if (!response.ok) {
    const body: unknown = await response
      .clone()
      .json()
      .catch(() => null)
    const code =
      body &&
      typeof body === 'object' &&
      'code' in body &&
      typeof body.code === 'string'
        ? body.code
        : ''
    throw new StoredAudioError('http', response.status, code)
  }
  if (
    response.headers
      .get('Content-Type')
      ?.split(';', 1)[0]
      .trim()
      .toLowerCase() !== 'audio/wav'
  )
    throw new StoredAudioError('invalid')
  let audio: Blob
  try {
    audio = await response.blob()
  } catch (error) {
    fetchSignal.throwIfAborted()
    if (isAbortOrTimeout(error)) throw error
    throw new StoredAudioError('network')
  }
  let valid: boolean
  try {
    valid = await validWav(audio)
  } catch (error) {
    fetchSignal.throwIfAborted()
    if (isAbortOrTimeout(error)) throw error
    throw new StoredAudioError('network')
  }
  if (!audio.size || !valid) throw new StoredAudioError('invalid')
  fetchSignal.throwIfAborted()
  // Response.blob() may come from a different fetch realm (notably Node's
  // undici in tests). Re-wrap it so callers receive the app's Blob class.
  return new Blob([audio], { type: audio.type || 'audio/wav' })
}

async function validWav(blob: Blob): Promise<boolean> {
  if (blob.size < 44 || blob.size > 64 * 1024 * 1024) return false
  const bytes = new DataView(await blob.arrayBuffer())
  const text = (offset: number, length: number) =>
    String.fromCharCode(...new Uint8Array(bytes.buffer, offset, length))
  if (
    text(0, 4) !== 'RIFF' ||
    text(8, 4) !== 'WAVE' ||
    bytes.getUint32(4, true) + 8 !== blob.size
  )
    return false
  let position = 12
  let blockAlign = 0
  let dataSize = 0
  let hasFormat = false
  while (position < blob.size) {
    if (position + 8 > blob.size) return false
    const chunkSize = bytes.getUint32(position + 4, true)
    const start = position + 8
    const end = start + chunkSize
    if (end > blob.size) return false
    const chunk = text(position, 4)
    if (chunk === 'fmt ') {
      if (chunkSize < 16) return false
      let format = bytes.getUint16(start, true)
      const channels = bytes.getUint16(start + 2, true)
      const rate = bytes.getUint32(start + 4, true)
      const byteRate = bytes.getUint32(start + 8, true)
      blockAlign = bytes.getUint16(start + 12, true)
      const bits = bytes.getUint16(start + 14, true)
      if (format === 0xfffe) {
        if (chunkSize < 40 || bytes.getUint16(start + 16, true) < 22)
          return false
        const validBits = bytes.getUint16(start + 18, true)
        if (!validBits || validBits > bits) return false
        const subtype = new Uint8Array(bytes.buffer, start + 24, 16)
        const guidSuffix = [0, 0, 0, 0, 16, 0, 128, 0, 0, 170, 0, 56, 155, 113]
        if (!guidSuffix.every((value, index) => subtype[index + 2] === value))
          return false
        format = bytes.getUint16(start + 24, true)
      }
      if (
        !(
          (format === 1 && [8, 16, 24, 32].includes(bits)) ||
          (format === 3 && [32, 64].includes(bits))
        ) ||
        !channels ||
        !rate ||
        blockAlign !== (channels * bits) / 8 ||
        byteRate !== rate * blockAlign
      )
        return false
      hasFormat = true
    }
    if (chunk === 'data') dataSize = chunkSize
    position = end + (chunkSize % 2)
    if (position > blob.size) return false
  }
  return (
    hasFormat && dataSize > 0 && blockAlign > 0 && dataSize % blockAlign === 0
  )
}
