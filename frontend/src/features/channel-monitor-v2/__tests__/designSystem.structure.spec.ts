import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { availabilityBadgeClass, availabilityBarClass } from '../monitorFormat'

const root = resolve(__dirname, '../../..')

function read(rel: string) {
  return readFileSync(resolve(root, rel), 'utf8')
}

describe('channel-monitor-v2 design system structure', () => {
  it('uses the compact V2 status page as the only passive user view', () => {
    const wrapper = read('views/user/ChannelStatusView.vue')
    const src = read('views/user/ChannelStatusV2View.vue')

    expect(wrapper).toContain('<ChannelStatusV2View v-else />')
    expect(wrapper).not.toContain('monitor_view')
    expect(src).toContain('page-title')
    expect(src).toContain('glass-card')
    expect(src).toContain('btn btn-secondary')
    expect(src).toContain('class="tab')
    expect(src).toContain('tab-active')
    expect(src).toContain('badge badge-warning')
    expect(src).toContain('ChannelMonitorV2Card')
  })

  it('V2 timeline keeps hover details in a stable tooltip layer', () => {
    const src = read('components/user/monitor/ChannelMonitorV2Timeline.vue')
    expect(src).toContain('v2-bar-hitbox')
    expect(src).toContain('is-neighbor')
    expect(src).toContain('v2-timeline-tooltip')
    expect(src).not.toContain(':title="bar.title"')
  })

  it('MonitorSettingsPanel uses project page and control utilities', () => {
    const src = read('features/channel-monitor-v2/MonitorSettingsPanel.vue')
    expect(src).toContain('page-header')
    expect(src).toContain('btn btn-primary')
    expect(src).toContain('class="card')
    expect(src).toContain('tab-active')
    expect(src).toMatch(/max-h-\[min\(40vh/)
  })

  it('admin ChannelMonitorView keeps the V2 configuration tab', () => {
    const src = read('views/admin/ChannelMonitorView.vue')
    expect(src).toContain('page-header')
    expect(src).toContain('page-title')
    expect(src).toContain('class="tabs')
    expect(src).toContain('tab-active')
    expect(src).toContain('MonitorSettingsPanel')
  })
})

describe('availabilityBadgeClass', () => {
  it('uses the requested availability color bands', () => {
    expect(availabilityBadgeClass(95)).toContain('bg-emerald-700')
    expect(availabilityBadgeClass(90)).toContain('bg-emerald-700')
    expect(availabilityBadgeClass(89.9)).toContain('bg-emerald-100')
    expect(availabilityBadgeClass(80)).toContain('bg-emerald-100')
    expect(availabilityBadgeClass(79.9)).toContain('bg-yellow-100')
    expect(availabilityBadgeClass(60)).toContain('bg-yellow-100')
    expect(availabilityBadgeClass(59.9)).toContain('bg-amber-200')
    expect(availabilityBadgeClass(50)).toContain('bg-amber-200')
    expect(availabilityBadgeClass(49.9)).toContain('bg-red-600')
    expect(availabilityBadgeClass(30)).toContain('bg-red-600')
    expect(availabilityBadgeClass(29.9)).toContain('bg-gray-950')
    expect(availabilityBarClass(95)).toContain('bg-emerald-600')
    expect(availabilityBarClass(80)).toContain('bg-emerald-400')
    expect(availabilityBarClass(60)).toContain('bg-yellow-300')
    expect(availabilityBarClass(50)).toContain('bg-amber-400')
    expect(availabilityBarClass(30)).toContain('bg-red-500')
    expect(availabilityBarClass(29.9)).toContain('bg-gray-950')
  })
})
