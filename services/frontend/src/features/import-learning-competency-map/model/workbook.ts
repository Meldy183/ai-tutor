import { unzipSync } from 'fflate'

export async function hasMultipleXlsxSheets(file: File): Promise<boolean> {
  let tooLarge = false
  const archive = unzipSync(new Uint8Array(await file.arrayBuffer()), {
    filter: (entry) => {
      if (entry.name !== 'xl/workbook.xml') return false
      if (entry.originalSize > 1_048_576) {
        tooLarge = true
        return false
      }
      return true
    },
  })
  if (tooLarge) throw new Error('XLSX workbook metadata exceeds the limit')
  const workbook = archive['xl/workbook.xml']
  if (!workbook) return false
  const xml = new TextDecoder().decode(workbook)
  return (xml.match(/<(?:[A-Za-z_][\w.-]*:)?sheet(?=[\s/>])/g) ?? []).length > 1
}
