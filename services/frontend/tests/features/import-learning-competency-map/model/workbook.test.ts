import { zipSync } from 'fflate'
import { expect, it } from 'vitest'
import { hasMultipleXlsxSheets } from '@/features/import-learning-competency-map'

function workbook(sheetNames: string[]) {
  const sheets = sheetNames
    .map(
      (name, index) =>
        `<sheet name="${name}" sheetId="${index + 1}" r:id="rId${index + 1}"/>`,
    )
    .join('')
  const archive = zipSync({
    'xl/workbook.xml': new TextEncoder().encode(
      `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheets>${sheets}</sheets></workbook>`,
    ),
  })
  return new File([archive], 'map.xlsx')
}

it('detects when one XLSX upload contains both course map sheets', async () => {
  await expect(
    hasMultipleXlsxSheets(workbook(['Geek', 'Обучение'])),
  ).resolves.toBe(true)
  await expect(hasMultipleXlsxSheets(workbook(['Карта']))).resolves.toBe(false)
})
