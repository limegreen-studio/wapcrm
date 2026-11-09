<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { PageHeader } from '@/components/shared'
import { BarChart3, Download } from 'lucide-vue-next'
import { usePipelinesStore } from '@/stores/pipelines'
import { pipelinesService, type PipelineReport, type ReportGroup, type ReportParams } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'
import { formatDuration } from '@/lib/pipelineBoard'

const { t } = useI18n()
const pipelinesStore = usePipelinesStore()

function dayInput(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

const pipelineId = ref('')
const group = ref<'day' | 'week' | 'month'>('day')
const from = ref(dayInput(new Date(Date.now() - 30 * 86_400_000)))
const to = ref(dayInput(new Date()))
const report = ref<PipelineReport | null>(null)

function params(): ReportParams {
  const end = new Date(`${to.value}T00:00:00`)
  end.setDate(end.getDate() + 1)
  return { from: new Date(`${from.value}T00:00:00`).toISOString(), to: end.toISOString(), group: group.value }
}

async function load() {
  if (!pipelineId.value || !from.value || !to.value) return
  try {
    report.value = unwrapResponse<PipelineReport>(await pipelinesService.report(pipelineId.value, params()))
  } catch (e) {
    toast.error(getErrorMessage(e, t('reports.failed')))
  }
}

async function exportCsv() {
  try {
    const response = await pipelinesService.reportCsv(pipelineId.value, params())
    const url = URL.createObjectURL(response.data as Blob)
    const link = document.createElement('a')
    link.href = url
    link.download = 'pipeline-report.csv'
    link.click()
    URL.revokeObjectURL(url)
  } catch (e) {
    toast.error(getErrorMessage(e, t('reports.failed')))
  }
}

watch([pipelineId, group, from, to], load)

onMounted(async () => {
  try {
    await pipelinesStore.fetchPipelines()
    pipelineId.value = pipelinesStore.defaultPipeline?.id ?? ''
  } catch (e) {
    toast.error(getErrorMessage(e, t('reports.failed')))
  }
})

function periodLabel(iso: string): string {
  return new Date(iso).toLocaleDateString([], { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })
}

function conversion(row: ReportGroup): string {
  const closed = row.won + row.lost
  return closed ? `${Math.round((row.won / closed) * 100)}%` : '—'
}

const groupTables = computed(() => report.value
  ? [
      { key: 'agent', title: t('reports.byAgent'), rows: report.value.by_agent, empty: t('reports.unassigned') },
      { key: 'source', title: t('reports.bySource'), rows: report.value.by_source, empty: t('reports.noSource') }
    ]
  : [])
</script>

<template>
  <div class="flex flex-col h-full bg-background light:bg-gray-50">
    <PageHeader
      :title="$t('reports.title')"
      :description="$t('reports.subtitle')"
      :icon="BarChart3"
      icon-gradient="bg-gradient-to-br from-sky-500 to-indigo-600 shadow-sky-500/20"
    >
      <template #actions>
        <Button variant="outline" size="sm" :disabled="!report" @click="exportCsv">
          <Download class="h-4 w-4 mr-2" />{{ $t('reports.export') }}
        </Button>
      </template>
    </PageHeader>

    <div class="flex flex-wrap items-end gap-3 px-4 sm:px-6 py-3 border-b border-white/[0.08] light:border-gray-200">
      <div v-if="pipelinesStore.pipelines.length > 1" class="space-y-1">
        <Label>{{ $t('pipeline.selectPipeline') }}</Label>
        <Select v-model="pipelineId">
          <SelectTrigger class="w-48"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="p in pipelinesStore.pipelines" :key="p.id" :value="p.id">{{ p.name }}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div class="space-y-1">
        <Label for="report-from">{{ $t('reports.from') }}</Label>
        <Input id="report-from" v-model="from" type="date" class="w-40" />
      </div>
      <div class="space-y-1">
        <Label for="report-to">{{ $t('reports.to') }}</Label>
        <Input id="report-to" v-model="to" type="date" class="w-40" />
      </div>
      <div class="space-y-1">
        <Label>{{ $t('reports.group') }}</Label>
        <Select v-model="group">
          <SelectTrigger class="w-32"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="day">{{ $t('reports.day') }}</SelectItem>
            <SelectItem value="week">{{ $t('reports.week') }}</SelectItem>
            <SelectItem value="month">{{ $t('reports.month') }}</SelectItem>
          </SelectContent>
        </Select>
      </div>
    </div>

    <div v-if="report" class="flex-1 overflow-y-auto p-4 sm:p-6 space-y-6 max-w-5xl text-sm text-white light:text-gray-900">
      <section>
        <h2 class="mb-2 font-medium">{{ $t('reports.byPeriod') }}</h2>
        <p v-if="!report.by_period.length" class="text-white/50 light:text-gray-500">{{ $t('reports.empty') }}</p>
        <div v-else class="overflow-x-auto rounded-lg border border-white/[0.08] light:border-gray-200">
          <table class="w-full text-left">
            <thead class="text-xs text-white/50 light:text-gray-500">
              <tr><th class="px-3 py-2 font-medium">{{ $t('reports.period') }}</th><th class="px-3 py-2 font-medium text-right">{{ $t('reports.leads') }}</th><th class="px-3 py-2 font-medium text-right">{{ $t('pipeline.kind.won') }}</th><th class="px-3 py-2 font-medium text-right">{{ $t('pipeline.kind.lost') }}</th></tr>
            </thead>
            <tbody class="tabular-nums">
              <tr v-for="row in report.by_period" :key="row.period" class="border-t border-white/[0.06] light:border-gray-100">
                <td class="px-3 py-2">{{ periodLabel(row.period) }}</td>
                <td class="px-3 py-2 text-right">{{ row.leads }}</td>
                <td class="px-3 py-2 text-right">{{ row.won }}</td>
                <td class="px-3 py-2 text-right">{{ row.lost }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section v-for="table in groupTables" :key="table.key">
        <h2 class="mb-2 font-medium">{{ table.title }}</h2>
        <p v-if="!table.rows.length" class="text-white/50 light:text-gray-500">{{ $t('reports.empty') }}</p>
        <div v-else class="overflow-x-auto rounded-lg border border-white/[0.08] light:border-gray-200">
          <table class="w-full text-left">
            <thead class="text-xs text-white/50 light:text-gray-500">
              <tr><th class="px-3 py-2 font-medium">{{ $t('reports.name') }}</th><th class="px-3 py-2 font-medium text-right">{{ $t('reports.leads') }}</th><th class="px-3 py-2 font-medium text-right">{{ $t('pipeline.kind.won') }}</th><th class="px-3 py-2 font-medium text-right">{{ $t('pipeline.kind.lost') }}</th><th class="px-3 py-2 font-medium text-right">{{ $t('pipeline.stats.winRate') }}</th><th class="px-3 py-2 font-medium text-right">{{ $t('reports.wonValue') }}</th></tr>
            </thead>
            <tbody class="tabular-nums">
              <tr v-for="row in table.rows" :key="row.key" class="border-t border-white/[0.06] light:border-gray-100">
                <td class="px-3 py-2">{{ row.name || table.empty }}</td>
                <td class="px-3 py-2 text-right">{{ row.leads }}</td>
                <td class="px-3 py-2 text-right">{{ row.won }}</td>
                <td class="px-3 py-2 text-right">{{ row.lost }}</td>
                <td class="px-3 py-2 text-right">{{ conversion(row) }}</td>
                <td class="px-3 py-2 text-right">{{ row.won_value.toLocaleString() }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section>
        <h2 class="mb-2 font-medium">{{ $t('reports.stageTimes') }}</h2>
        <p v-if="!report.stage_times.length" class="text-white/50 light:text-gray-500">{{ $t('reports.empty') }}</p>
        <ul v-else class="divide-y divide-white/[0.06] light:divide-gray-100 rounded-lg border border-white/[0.08] light:border-gray-200">
          <li v-for="row in report.stage_times" :key="row.stage" class="flex items-center justify-between px-3 py-2">
            <span>{{ row.stage }}</span>
            <span class="tabular-nums text-white/60 light:text-gray-600">{{ formatDuration(row.avg_seconds) }} · {{ $t('reports.moves', { n: row.moves }) }}</span>
          </li>
        </ul>
      </section>
    </div>
  </div>
</template>
