// 日期输入按项目业务时区保存；未编辑的存量精确时刻原样保留。
export function taskDate(value: string | null | undefined, timezone: string) {
  if (!value) return ''
  if (/^\d{4}-\d{2}-\d{2}$/.test(value)) return value
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: timezone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(new Date(value))
  return `${parts.find((item) => item.type === 'year')!.value}-${parts.find((item) => item.type === 'month')!.value}-${parts.find((item) => item.type === 'day')!.value}`
}
export function taskDateInstant(date: string, timezone: string, end = false) {
  const wall = Date.parse(`${date}T${end ? '23:59:59.999' : '00:00:00.000'}Z`)
  let instant = wall
  for (let index = 0; index < 3; index++) {
    const offset = new Intl.DateTimeFormat('en-US', {
      timeZone: timezone,
      timeZoneName: 'longOffset',
    })
      .formatToParts(new Date(instant))
      .find((item) => item.type === 'timeZoneName')!.value
    const match = offset.match(/GMT([+-])(\d{2}):(\d{2})/)
    const minutes = match
      ? (Number(match[2]) * 60 + Number(match[3])) * (match[1] === '+' ? 1 : -1)
      : 0
    const next = wall - minutes * 60000
    if (next === instant) break
    instant = next
  }
  if (taskDate(new Date(instant).toISOString(), timezone) !== date)
    throw new Error('该日期在项目业务时区不存在，请重新选择。')
  return new Date(instant).toISOString()
}
