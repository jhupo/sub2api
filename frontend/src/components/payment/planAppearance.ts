import type { PlanCardAppearance } from '@/types/payment'

export const planCardThemes = ['classic', 'mint', 'ruby'] as const
export const planCardMotions = ['none', 'shine', 'flow'] as const

export function normalizePlanCardAppearance(value?: Partial<PlanCardAppearance> | null): PlanCardAppearance {
  return {
    theme: value && planCardThemes.includes(value.theme as PlanCardAppearance['theme']) ? value.theme! : 'classic',
    motion: value && planCardMotions.includes(value.motion as PlanCardAppearance['motion']) ? value.motion! : 'none',
  }
}
