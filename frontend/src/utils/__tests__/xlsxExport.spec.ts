import { describe, expect, it } from 'vitest'
import * as XLSX from 'xlsx'

describe('SheetJS usage export compatibility', () => {
  it('round-trips paginated usage rows through the browser export API', () => {
    const headers = ['用户', 'Requested model', 'Input tokens', 'User billed', 'Request ID']
    const firstPage = ['user@example.com', 'gpt-5.6-sol', 120, '0.000123', 'req-001']
    const secondPage = ['测试用户', 'gpt-5.5', 0, '12.340000', 'req-002\ncontinued']
    const sheet = XLSX.utils.aoa_to_sheet([headers])
    XLSX.utils.sheet_add_aoa(sheet, [firstPage], { origin: -1 })
    XLSX.utils.sheet_add_aoa(sheet, [secondPage], { origin: -1 })
    const workbook = XLSX.utils.book_new()
    XLSX.utils.book_append_sheet(workbook, sheet, 'Usage')

    const bytes = XLSX.write(workbook, { bookType: 'xlsx', type: 'array' })
    const exportedBlob = new Blob([bytes], {
      type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'
    })
    expect(exportedBlob.size).toBeGreaterThan(0)
    const decoded = XLSX.read(bytes, { type: 'array' })

    expect(decoded.SheetNames).toEqual(['Usage'])
    expect(XLSX.utils.sheet_to_json(decoded.Sheets.Usage, { header: 1 })).toEqual([
      headers,
      firstPage,
      secondPage
    ])
    expect(decoded.Sheets.Usage.C2.t).toBe('n')
    expect(decoded.Sheets.Usage.D2.t).toBe('s')
  })

  it('keeps exported account text as cell values rather than formulas', () => {
    const values = ['=HYPERLINK("https://example.com")', '+1', '@name', '00123']
    const sheet = XLSX.utils.aoa_to_sheet([['Account'], ...values.map(value => [value])])
    const workbook = XLSX.utils.book_new()
    XLSX.utils.book_append_sheet(workbook, sheet, 'Usage')
    const decoded = XLSX.read(XLSX.write(workbook, { bookType: 'xlsx', type: 'array' }), { type: 'array' })

    expect(XLSX.utils.sheet_to_json(decoded.Sheets.Usage, { header: 1 })).toEqual([
      ['Account'],
      ...values.map(value => [value])
    ])
    expect(decoded.Sheets.Usage.A2.f).toBeUndefined()
    expect(decoded.Sheets.Usage.A2.t).toBe('s')
  })
})
