"use client"

import * as React from "react"
import { Loader2Icon, RefreshCwIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { adminApi } from "@/lib/api"

import { InlineError, type StatsResponse } from "./shared"

function percent(value: number) {
  return `${(value * 100).toFixed(1)}%`
}

export function OverviewPanel({ password }: { password: string }) {
  const [stats, setStats] = React.useState<StatsResponse | null>(null)
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState("")

  const load = React.useCallback(async () => {
    setLoading(true)
    setError("")
    const result = await adminApi<StatsResponse>(password, "/api/admin/stats")
    setLoading(false)
    if (!result.ok) {
      setError(result.message || "无法读取统计。")
      return
    }
    setStats(result.data)
  }, [password])

  React.useEffect(() => {
    void load()
  }, [load])

  const codes = stats?.codes ?? {}
  const daily = stats?.daily ?? []
  const maxDaily = Math.max(1, ...daily.map((item) => Math.max(item.created, item.redeemed, item.succeeded)))

  return (
    <div className="grid gap-4">
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">数据实时来自站点数据库。</p>
        <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
          {loading ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
          刷新
        </Button>
      </div>
      <InlineError>{error}</InlineError>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[
          { label: "兑换码总数", value: codes.total ?? 0 },
          { label: "未使用", value: codes.active ?? 0 },
          { label: "处理中", value: codes.processing ?? 0 },
          { label: "已完成", value: codes.succeeded ?? 0 },
        ].map((item) => (
          <Card key={item.label}>
            <CardContent className="py-4">
              <p className="text-xs text-muted-foreground">{item.label}</p>
              <p className="mt-1 text-2xl font-semibold tabular-nums">{item.value}</p>
            </CardContent>
          </Card>
        ))}
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">兑换率</CardTitle>
            <CardDescription>已绑定账号的兑换码占比</CardDescription>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold tabular-nums">{percent(stats?.rates.redeemed ?? 0)}</p>
            <p className="mt-1 text-xs text-muted-foreground">
              已兑换 {codes.redeemed ?? 0} / 共 {codes.total ?? 0}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="text-base">付款成功率</CardTitle>
            <CardDescription>已兑换中完成付款的比例</CardDescription>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold tabular-nums">{percent(stats?.rates.success ?? 0)}</p>
            <p className="mt-1 text-xs text-muted-foreground">
              已完成 {codes.succeeded ?? 0} / 已兑换 {codes.redeemed ?? 0}
            </p>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">最近 30 天</CardTitle>
          <CardDescription>灰色为新建，主色为已兑换，深色为已完成</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex h-32 items-end gap-[3px] overflow-x-auto" role="img" aria-label="最近 30 天兑换走势">
            {daily.map((item) => (
              <div
                key={item.date}
                className="flex h-full min-w-2 flex-1 flex-col justify-end gap-[2px]"
                title={`${item.date}：新建 ${item.created}，已兑换 ${item.redeemed}，完成 ${item.succeeded}`}
              >
                <div
                  className="w-full rounded-sm bg-muted-foreground/30"
                  style={{ height: `${(item.created / maxDaily) * 100}%` }}
                />
                <div
                  className="w-full rounded-sm bg-primary/45"
                  style={{ height: `${(item.redeemed / maxDaily) * 100}%` }}
                />
                <div
                  className="w-full rounded-sm bg-primary"
                  style={{ height: `${(item.succeeded / maxDaily) * 100}%` }}
                />
              </div>
            ))}
          </div>
        </CardContent>
      </Card>

      <Card className="p-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>套餐</TableHead>
              <TableHead>总数</TableHead>
              <TableHead>已完成</TableHead>
              <TableHead>完成率</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(stats?.months ?? []).map((row) => (
              <TableRow key={row.months}>
                <TableCell>{row.months} 个月</TableCell>
                <TableCell className="tabular-nums">{row.total}</TableCell>
                <TableCell className="tabular-nums">{row.succeeded}</TableCell>
                <TableCell className="tabular-nums">
                  {row.total ? percent(row.succeeded / row.total) : "—"}
                </TableCell>
              </TableRow>
            ))}
            {stats && stats.months.length === 0 ? (
              <TableRow>
                <TableCell colSpan={4} className="py-8 text-center text-sm text-muted-foreground">
                  暂无数据。
                </TableCell>
              </TableRow>
            ) : null}
          </TableBody>
        </Table>
      </Card>
    </div>
  )
}
