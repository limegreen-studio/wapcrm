<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { PageHeader, ErrorState } from '@/components/shared'
import { UsersRound } from 'lucide-vue-next'
import { pipelinesService, type TeamReport, type TeamReportRow } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'
import { formatDuration } from '@/lib/pipelineBoard'

type Range = 'today' | 'week' | 'month'

const { t } = useI18n()
const range = ref<Range>('today')
const report = ref<TeamReport | null>(null)
const error = ref(false)
const ranges: Range[] = ['today', 'week', 'month']

function fromFor(value: Range): Date {
  const d = new Date()
  d.setHours(0, 0, 0, 0)
  if (value === 'week') d.setDate(d.getDate() - 6)
  if (value === 'month') d.setDate(d.getDate() - 29)
  return d
}

async function load() {
  error.value = false
  try {
    const response = await pipelinesService.teamReport({
      from: fromFor(range.value).toISOString(),
      to: new Date().toISOString()
    })
    report.value = unwrapResponse<TeamReport>(response)
  } catch (e) {
    error.value = true
    toast.error(getErrorMessage(e, t('team.failed')))
  }
}

watch(range, load)
onMounted(load)

const summary = computed(() => report.value
  ? [
      { key: 'newLeads', value: report.value.new_leads },
      { key: 'won', value: report.value.won },
      { key: 'lost', value: report.value.lost },
      { key: 'unowned', value: report.value.unowned },
      { key: 'openValue', value: report.value.open_value.toLocaleString() },
      { key: 'forecast', value: Math.round(report.value.forecast).toLocaleString() }
    ]
  : [])

// Agents with nothing to show stay out of the way
const agents = computed(() => (report.value?.agents ?? []).filter(a =>
  a.open_leads || a.overdue_tasks || a.tasks_done || a.messages_sent || a.won || a.monthly_target
))

function responseTime(row: TeamReportRow): string {
  return row.avg_response_seconds == null ? '—' : formatDuration(row.avg_response_seconds)
}

function targetLabel(row: TeamReportRow): string {
  const won = row.won_value_month.toLocaleString()
  return row.monthly_target ? `${won} / ${row.monthly_target.toLocaleString()}` : won
}

const columns: { key: string; value: (row: TeamReportRow) => string | number; alert?: (row: TeamReportRow) => boolean }[] = [
  { key: 'openLeads', value: r => r.open_leads },
  { key: 'awaitingReply', value: r => r.awaiting_reply, alert: r => r.awaiting_reply > 0 },
  { key: 'untouched', value: r => r.untouched, alert: r => r.untouched > 0 },
  { key: 'overdue', value: r => r.overdue_tasks, alert: r => r.overdue_tasks > 0 },
  { key: 'tasksDone', value: r => r.tasks_done },
  { key: 'messages', value: r => r.messages_sent },
  { key: 'responseTime', value: responseTime },
  { key: 'won', value: r => r.won },
  { key: 'target', value: targetLabel }
]
</script>

<template>
  <div class="flex flex-col h-full bg-background light:bg-gray-50">
    <PageHeader
      :title="$t('team.title')"
      :description="$t('team.subtitle')"
      :icon="UsersRound"
      icon-gradient="bg-gradient-to-br from-violet-500 to-purple-600 shadow-violet-500/20"
    />

    <div class="px-4 sm:px-6 py-3 border-b border-white/[0.08] light:border-gray-200">
      <div role="tablist" class="inline-flex gap-1 rounded-lg bg-white/[0.04] light:bg-gray-100 p-1">
        <button
          v-for="r in ranges"
          :key="r"
          role="tab"
          :aria-selected="range === r"
          class="rounded-md px-3 py-1.5 text-sm transition-colors min-h-9"
          :class="range === r
            ? 'bg-white/[0.08] text-white light:bg-white light:text-gray-900 shadow-sm'
            : 'text-white/50 hover:text-white/80 light:text-gray-500 light:hover:text-gray-800'"
          @click="range = r"
        >{{ $t(`team.range.${r}`) }}</button>
      </div>
    </div>

    <ErrorState
      v-if="error"
      :title="$t('common.loadErrorTitle')"
      :description="$t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="load"
    />

    <div v-else-if="report" class="flex-1 overflow-y-auto p-4 sm:p-6 space-y-6 text-sm text-white light:text-gray-900">
      <dl class="grid grid-cols-2 lg:grid-cols-3 gap-3 max-w-5xl">
        <div
          v-for="item in summary"
          :key="item.key"
          class="rounded-xl border border-white/[0.08] light:border-gray-200 bg-white/[0.02] light:bg-white p-4"
        >
          <dt class="text-xs text-white/50 light:text-gray-500">{{ $t(`team.summary.${item.key}`) }}</dt>
          <dd class="mt-1 text-2xl font-semibold tabular-nums">{{ item.value }}</dd>
        </div>
      </dl>

      <p v-if="!agents.length" class="py-10 text-center text-white/50 light:text-gray-500">{{ $t('team.empty') }}</p>

      <template v-else>
        <!-- Phone: one card per agent -->
        <ul class="md:hidden space-y-3">
          <li v-for="row in agents" :key="row.user_id" class="rounded-xl border border-white/[0.08] light:border-gray-200 bg-white/[0.02] light:bg-white p-4">
            <p class="font-medium">{{ row.name }}</p>
            <dl class="mt-2 grid grid-cols-2 gap-x-4 gap-y-1.5 text-xs">
              <div v-for="col in columns" :key="col.key" class="flex justify-between gap-2">
                <dt class="text-white/50 light:text-gray-500">{{ $t(`team.col.${col.key}`) }}</dt>
                <dd class="tabular-nums" :class="col.alert?.(row) ? 'text-amber-400 light:text-amber-700 font-medium' : ''">{{ col.value(row) }}</dd>
              </div>
            </dl>
          </li>
        </ul>

        <!-- Wider screens: table -->
        <div class="hidden md:block overflow-x-auto rounded-xl border border-white/[0.08] light:border-gray-200 max-w-5xl">
          <table class="w-full text-left">
            <thead class="text-xs text-white/50 light:text-gray-500">
              <tr>
                <th class="px-3 py-2 font-medium">{{ $t('reports.name') }}</th>
                <th v-for="col in columns" :key="col.key" class="px-3 py-2 font-medium text-right whitespace-nowrap">{{ $t(`team.col.${col.key}`) }}</th>
              </tr>
            </thead>
            <tbody class="tabular-nums">
              <tr v-for="row in agents" :key="row.user_id" class="border-t border-white/[0.06] light:border-gray-100">
                <td class="px-3 py-2 font-medium">{{ row.name }}</td>
                <td
                  v-for="col in columns"
                  :key="col.key"
                  class="px-3 py-2 text-right"
                  :class="col.alert?.(row) ? 'text-amber-400 light:text-amber-700 font-medium' : ''"
                >{{ col.value(row) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </div>
  </div>
</template>
