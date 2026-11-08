import { afterEach, expect, it, vi } from 'vitest'
import { fetchStoredAudio, transcribeRecording } from '@/shared/api'
import { validExtensibleWavBlob, validWavBlob } from '../../support/audio'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it('uploads recording as multipart file and reads actual transcription', async () => {
  const fetchMock = vi.fn().mockResolvedValue({
    ok: true,
    json: async () => ({ id: 'tr-1', text: 'Мой реальный ответ' }),
  })
  vi.stubGlobal('fetch', fetchMock)
  const signal = new AbortController().signal
  expect(
    await transcribeRecording(
      new Blob(['audio'], { type: 'audio/webm' }),
      signal,
    ),
  ).toEqual({ id: 'tr-1', text: 'Мой реальный ответ' })
  const [url, init] = fetchMock.mock.calls[0]
  expect(url).toBe('/api/v1/voice/transcriptions')
  expect(init.body.get('audio').name).toBe('answer.webm')
  expect(init.headers).toBeUndefined()
  expect(init.credentials).toBe('include')
  expect(init.signal).toBeInstanceOf(AbortSignal)
  expect(init.signal.aborted).toBe(false)
})

it('fetches stored WAV from an internal API path with cookie credentials', async () => {
  const wav = await validWavBlob().arrayBuffer()
  const fetchMock = vi
    .fn()
    .mockResolvedValue(
      new Response(wav, { headers: { 'Content-Type': 'audio/wav' } }),
    )
  vi.stubGlobal('fetch', fetchMock)
  const audio = await fetchStoredAudio(
    '/task-audio/audio-1/file',
    new AbortController().signal,
  )
  expect(audio.type).toBe('audio/wav')
  expect(await audio.arrayBuffer()).toEqual(wav)
  expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/task-audio/audio-1/file')
  expect(fetchMock.mock.calls[0][1].credentials).toBe('include')
  expect(fetchMock.mock.calls[0][1].cache).toBe('no-store')
  await expect(
    fetchStoredAudio(
      'https://evil.example/audio.wav',
      new AbortController().signal,
    ),
  ).rejects.toThrow()
  await expect(
    fetchStoredAudio('/task-audio/audio-1/other', new AbortController().signal),
  ).rejects.toThrow()
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValue(
        new Response('not wav', { headers: { 'Content-Type': 'text/plain' } }),
      ),
  )
  await expect(
    fetchStoredAudio('/task-audio/audio-1/file', new AbortController().signal),
  ).rejects.toMatchObject({ kind: 'invalid' })
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValue(
        new Response('', { headers: { 'Content-Type': 'audio/wav' } }),
      ),
  )
  await expect(
    fetchStoredAudio('/task-audio/audio-1/file', new AbortController().signal),
  ).rejects.toMatchObject({ kind: 'invalid' })
})

it('fetches canonical learning task audio from the registered learning route', async () => {
  const wav = await validWavBlob().arrayBuffer()
  const fetchMock = vi
    .fn()
    .mockResolvedValue(
      new Response(wav, { headers: { 'Content-Type': 'audio/wav' } }),
    )
  vi.stubGlobal('fetch', fetchMock)
  await fetchStoredAudio(
    '/learn/task-audio/audio-1/file',
    new AbortController().signal,
  )
  expect(fetchMock.mock.calls[0][0]).toBe(
    '/api/v1/learn/task-audio/audio-1/file',
  )
})

it('rejects a structurally broken WAV even when the response MIME is audio/wav', async () => {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      new Response('not a RIFF file', {
        headers: { 'Content-Type': 'audio/wav' },
      }),
    ),
  )
  await expect(
    fetchStoredAudio('/task-audio/audio-1/file', new AbortController().signal),
  ).rejects.toMatchObject({ kind: 'invalid' })
})

it('accepts a valid WAVE_FORMAT_EXTENSIBLE PCM file like the backend validator', async () => {
  const wav = await validExtensibleWavBlob().arrayBuffer()
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      new Response(wav, {
        headers: { 'Content-Type': 'audio/wav' },
      }),
    ),
  )
  const audio = await fetchStoredAudio(
    '/task-audio/audio-1/file',
    new AbortController().signal,
  )
  expect(audio.type).toBe('audio/wav')
  expect(await audio.arrayBuffer()).toEqual(wav)
})

it('preserves HTTP status and API code for stored-audio failures', async () => {
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValue(
        Response.json({ code: 'AUDIO_STORAGE_UNAVAILABLE' }, { status: 503 }),
      ),
  )
  await expect(
    fetchStoredAudio('/task-audio/audio-1/file', new AbortController().signal),
  ).rejects.toMatchObject({
    kind: 'http',
    status: 503,
    code: 'AUDIO_STORAGE_UNAVAILABLE',
  })
})

it('classifies a truncated stored-audio response as a repairable network failure', async () => {
  const response = new Response(new Uint8Array([1, 2, 3]), {
    headers: { 'Content-Type': 'audio/wav' },
  })
  vi.spyOn(response, 'blob').mockRejectedValue(new TypeError('truncated body'))
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response))
  await expect(
    fetchStoredAudio('/task-audio/audio-1/file', new AbortController().signal),
  ).rejects.toMatchObject({ kind: 'network' })
})

it('preserves an aborted body read instead of classifying it for repair', async () => {
  const response = new Response(new Uint8Array([1, 2, 3]), {
    headers: { 'Content-Type': 'audio/wav' },
  })
  vi.spyOn(response, 'blob').mockRejectedValue(
    new DOMException('stopped', 'AbortError'),
  )
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response))
  await expect(
    fetchStoredAudio('/task-audio/audio-1/file', new AbortController().signal),
  ).rejects.toMatchObject({ name: 'AbortError' })
})

it('rejects service failures and empty transcriptions instead of supplying a mock', async () => {
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockImplementation(async () =>
        Response.json({ code: 'NO_SPEECH_DETECTED' }, { status: 422 }),
      ),
  )
  await expect(
    transcribeRecording(new Blob(['a']), new AbortController().signal),
  ).rejects.toMatchObject({ status: 422, code: 'NO_SPEECH_DETECTED' })
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ id: 'tr-1', text: ' ' }),
    }),
  )
  await expect(
    transcribeRecording(new Blob(['a']), new AbortController().signal),
  ).rejects.toThrow('empty')
})

it('sends production requests through the same origin without build-time env', async () => {
  vi.stubEnv('DEV', false)
  vi.stubEnv('VITE_API_BASE_URL', '')
  vi.resetModules()
  try {
    const api = await import('@/shared/api')
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ id: 'tr-1', text: 'Ответ' }),
    })
    vi.stubGlobal('fetch', fetchMock)
    await expect(
      api.transcribeRecording(
        new Blob(['audio']),
        new AbortController().signal,
      ),
    ).resolves.toEqual({ id: 'tr-1', text: 'Ответ' })
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/voice/transcriptions')
  } finally {
    vi.unstubAllEnvs()
    vi.resetModules()
  }
})

it('preserves unauthorized status for a useful session error', async () => {
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockImplementation(async () =>
        Response.json({ code: 'UNAUTHORIZED' }, { status: 401 }),
      ),
  )
  await expect(
    transcribeRecording(new Blob(['a']), new AbortController().signal),
  ).rejects.toMatchObject({ status: 401 })
})

function delayedTranscription(delay: number) {
  vi.useFakeTimers()
  // Native AbortSignal timers use real time in Node; connect them to the clock.
  vi.spyOn(AbortSignal, 'timeout').mockImplementation((milliseconds) => {
    const controller = new AbortController()
    setTimeout(
      () => controller.abort(new DOMException('Timed out', 'TimeoutError')),
      milliseconds,
    )
    return controller.signal
  })
  vi.stubGlobal(
    'fetch',
    vi.fn((_url, options: RequestInit) => {
      const signal = options.signal!
      return new Promise<Response>((resolve, reject) => {
        const timer = setTimeout(
          () => resolve(Response.json({ id: 'tr-long', text: 'Ответ' })),
          delay,
        )
        const abort = () => {
          clearTimeout(timer)
          reject(signal.reason)
        }
        if (signal.aborted) abort()
        else signal.addEventListener('abort', abort, { once: true })
      })
    }),
  )
}

it('keeps waiting for recognition after two minutes within the backend budget', async () => {
  delayedTranscription(395_000)
  const result = transcribeRecording(
    new Blob(['audio']),
    new AbortController().signal,
  )
  const checked = expect(result).resolves.toEqual({
    id: 'tr-long',
    text: 'Ответ',
  })
  await vi.advanceTimersByTimeAsync(395_000)
  await checked
})

it('stops recognition after the browser budget is exhausted', async () => {
  delayedTranscription(460_000)
  const result = transcribeRecording(
    new Blob(['audio']),
    new AbortController().signal,
  )
  const checked = expect(result).rejects.toMatchObject({ name: 'TimeoutError' })
  await vi.advanceTimersByTimeAsync(430_000)
  await checked
})

it('allows the caller to cancel recognition while it waits in the queue', async () => {
  delayedTranscription(395_000)
  const controller = new AbortController()
  const result = transcribeRecording(new Blob(['audio']), controller.signal)
  const checked = expect(result).rejects.toMatchObject({ name: 'AbortError' })
  await vi.advanceTimersByTimeAsync(150_000)
  controller.abort()
  await checked
})
