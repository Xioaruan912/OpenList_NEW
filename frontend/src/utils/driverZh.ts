import zhDrivers from "~/lang/zh-CN/drivers.json"

const zhNames = (zhDrivers as { drivers: Record<string, string> }).drivers

export function driverZhName(en: string): string {
  return zhNames[en] || en
}

export function parseDriversShow(value?: string): string[] {
  return (value || "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean)
}

export function isDriverShown(en: string, allow: string[]): boolean {
  if (allow.length === 0) return true
  const zh = driverZhName(en)
  return allow.includes(zh) || allow.includes(en)
}