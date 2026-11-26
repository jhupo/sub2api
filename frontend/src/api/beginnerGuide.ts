import { apiClient } from './client'
import type { GuideChapterId } from '@/utils/beginnerGuide'

export interface GuideChapterContent {
  title: string
  description: string
  markdown: string
}

export type GuideLanguage = 'zh' | 'en'
export type GuideContent = Partial<Record<GuideLanguage, Partial<Record<GuideChapterId, GuideChapterContent>>>>

export interface GuideDocument {
  revision: number
  draft: GuideContent
  published: GuideContent
  updated_at: string | null
  published_at: string | null
}

export interface PublishedGuide {
  content: GuideContent
  published_at: string | null
}

export const beginnerGuideAPI = {
  async published() {
    return (await apiClient.get<PublishedGuide>('/beginner-guide')).data
  },
  async admin() {
    return (await apiClient.get<GuideDocument>('/admin/settings/beginner-guide')).data
  },
  async save(revision: number, content: GuideContent, publish = false) {
    return (await apiClient.request<GuideDocument>({
      url: `/admin/settings/beginner-guide/${publish ? 'publish' : 'draft'}`,
      method: publish ? 'POST' : 'PUT',
      data: { revision, content },
    })).data
  },
}
