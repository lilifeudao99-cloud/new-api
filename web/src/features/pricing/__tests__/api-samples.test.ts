import { describe, expect, test } from 'vitest'

import {
  buildVideoSample,
  type VideoSampleContext,
  type VideoSampleLang,
} from '../lib/video-api-sample'

const sampleContext: VideoSampleContext = {
  baseUrl: 'https://ailili.chat',
  apiKeyEnv: 'NEW_API_KEY',
  modelName: 'seedance-2.5',
  endpointType: 'openai-video',
  endpointPath: '/v1/videos',
}

describe('video API code samples', () => {
  test.each<VideoSampleLang>(['curl', 'python', 'typescript', 'javascript'])(
    'polls the video task to a terminal status in the %s sample',
    (lang) => {
      const sample = buildVideoSample(lang, sampleContext)

      expect(sample).toContain('/v1/videos')
      expect(sample).toContain('completed')
      expect(sample).toContain('failed')
      expect(sample).toContain('120')
      expect(sample).toContain('5')
    }
  )
})
