import { z } from '../../api/validation'

export const briefReceipts = z.array(z.object({
  business_date: z.iso.date(),
  version: z.number().int().positive(),
}))

export function BriefReceiptLinks({ project, receipts }: { project: string; receipts: z.infer<typeof briefReceipts> }) {
  return receipts.map((receipt, index) => (
    <p key={index}>
      <a href={'/#/projects/' + encodeURIComponent(project) + '/reports/brief/' + receipt.business_date}>
        查看 {receipt.business_date} 简报
      </a>
      {' · 保存版本 ' + receipt.version}
    </p>
  ))
}
