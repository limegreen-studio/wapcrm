<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { PageHeader, ErrorState } from '@/components/shared'
import { ListTodo, Trash2, MessageSquare, User, CalendarClock, CalendarPlus, Pencil } from 'lucide-vue-next'
import FollowUpDialog from '@/components/tasks/FollowUpDialog.vue'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Input } from '@/components/ui/input'
import { DUE_PRESETS, toLocalInput } from '@/lib/taskDue'
import { tasksService, type Task, type TaskListParams } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'
import { formatDateTime } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth'
import { enablePush } from '@/lib/push'
import { useUsersStore } from '@/stores/users'
import { wsService } from '@/services/websocket'

type Bucket = 'overdue' | 'today' | 'upcoming' | 'open' | 'done'

const PAGE_LIMIT = 100

const { t } = useI18n()
const authStore = useAuthStore()
const usersStore = useUsersStore()

const bucket = ref<Bucket>('overdue')
const owner = ref<'mine' | 'all'>('mine')
const tasks = ref<Task[]>([])
const total = ref(0)
const counts = ref<Record<'overdue' | 'today', number>>({ overdue: 0, today: 0 })
const isLoading = ref(false)
const error = ref(false)
const busyIds = ref(new Set<string>())

const canWrite = computed(() => authStore.hasPermission('chat', 'write'))
// Reassigning needs the user list (users:read), same rule as the chat assign menu
const canReassign = computed(() => canWrite.value && authStore.hasPermission('users', 'read'))
const UNASSIGNED = '__none__'
const activeUsers = computed(() => usersStore.users.filter(u => u.is_active))

async function reassign(task: Task, value: unknown) {
  const next = value === UNASSIGNED ? null : String(value)
  if (next === (task.assignee_id ?? null)) return
  busyIds.value.add(task.id)
  try {
    const response = await tasksService.update(task.id, next ? { assignee_id: next } : { clear_assignee: true })
    const updated = unwrapResponse<Task>(response)
    // "My tasks" no longer includes a task given away
    if (owner.value === 'mine' && updated.assignee_id !== authStore.user?.id) {
      tasks.value = tasks.value.filter(x => x.id !== task.id)
      total.value = Math.max(0, total.value - 1)
      loadCounts()
    } else {
      Object.assign(task, updated)
    }
    toast.success(t('tasks.reassigned'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('tasks.failedSave')))
  } finally {
    busyIds.value.delete(task.id)
  }
}

const buckets: Bucket[] = ['overdue', 'today', 'upcoming', 'open', 'done']

function endOfToday(): Date {
  const d = new Date()
  d.setHours(23, 59, 59, 999)
  return d
}

// Query params for a bucket. Day boundaries use the browser's timezone.
function paramsFor(b: Bucket): TaskListParams {
  const params: TaskListParams = { status: b === 'done' ? 'done' : 'open' }
  if (owner.value === 'mine') params.assignee_id = 'me'
  const now = new Date().toISOString()
  if (b === 'overdue') params.due_before = now
  if (b === 'today') {
    params.due_after = now
    params.due_before = endOfToday().toISOString()
  }
  if (b === 'upcoming') params.due_after = endOfToday().toISOString()
  return params
}

async function loadTasks() {
  isLoading.value = true
  error.value = false
  try {
    const response = await tasksService.list({ ...paramsFor(bucket.value), limit: PAGE_LIMIT })
    const data = unwrapResponse<{ tasks: Task[]; total: number }>(response)
    tasks.value = data.tasks || []
    total.value = data.total ?? tasks.value.length
  } catch (e) {
    toast.error(getErrorMessage(e, t('tasks.failedLoad')))
    error.value = true
  } finally {
    isLoading.value = false
  }
}

async function loadCounts() {
  try {
    const [overdue, today] = await Promise.all(
      (['overdue', 'today'] as const).map(b => tasksService.list({ ...paramsFor(b), limit: 1 }))
    )
    counts.value = {
      overdue: unwrapResponse<{ total: number }>(overdue).total ?? 0,
      today: unwrapResponse<{ total: number }>(today).total ?? 0
    }
  } catch {
    // counts are a convenience; the list itself reports errors
  }
}

function refresh() {
  loadTasks()
  loadCounts()
}

watch([bucket, owner], () => loadTasks())
watch(owner, () => loadCounts())

async function toggleDone(task: Task, done: boolean) {
  busyIds.value.add(task.id)
  try {
    await tasksService.update(task.id, { done })
    // Leaves the current list (open <-> done)
    tasks.value = tasks.value.filter(x => x.id !== task.id)
    total.value = Math.max(0, total.value - 1)
    loadCounts()
    if (done) toast.success(t('tasks.markedDone'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('tasks.failedSave')))
  } finally {
    busyIds.value.delete(task.id)
  }
}

// Reschedule: one popover open at a time
const rescheduleId = ref<string | null>(null)
const rescheduleValue = ref('')

function openReschedule(task: Task, open: boolean) {
  if (!open) {
    if (rescheduleId.value === task.id) rescheduleId.value = null
    return
  }
  rescheduleId.value = task.id
  rescheduleValue.value = task.due_at ? toLocalInput(new Date(task.due_at)) : ''
}

// Bucket the task belongs to after a due change, to drop it from this list
function bucketFor(dueAt?: string): Bucket {
  if (!dueAt) return 'open'
  const due = new Date(dueAt).getTime()
  if (due < Date.now()) return 'overdue'
  const end = new Date()
  end.setHours(23, 59, 59, 999)
  return due <= end.getTime() ? 'today' : 'upcoming'
}

async function reschedule(task: Task, localValue: string) {
  busyIds.value.add(task.id)
  try {
    const body = localValue
      ? { due_at: new Date(localValue).toISOString() }
      : { clear_due_at: true }
    const response = await tasksService.update(task.id, body)
    const updated = unwrapResponse<Task>(response)
    rescheduleId.value = null
    const stays = bucket.value === 'open' || bucket.value === bucketFor(updated.due_at)
    if (stays) {
      Object.assign(task, updated)
    } else {
      tasks.value = tasks.value.filter(x => x.id !== task.id)
      total.value = Math.max(0, total.value - 1)
    }
    loadCounts()
    toast.success(t('tasks.rescheduled'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('tasks.failedSave')))
  } finally {
    busyIds.value.delete(task.id)
  }
}

async function removeTask(task: Task) {
  busyIds.value.add(task.id)
  try {
    await tasksService.delete(task.id)
    tasks.value = tasks.value.filter(x => x.id !== task.id)
    total.value = Math.max(0, total.value - 1)
    loadCounts()
  } catch (e) {
    toast.error(getErrorMessage(e, t('tasks.failedSave')))
  } finally {
    busyIds.value.delete(task.id)
  }
}

// Desktop alerts for due follow-ups; browsers require a click to ask
const canAskAlerts = ref(typeof Notification !== 'undefined' && Notification.permission === 'default')

async function enableAlerts() {
  try {
    const enabled = await enablePush()
    canAskAlerts.value = Notification.permission === 'default'
    if (enabled) toast.success(t('tasks.alertsEnabled'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('tasks.failedSave')))
  }
}

const editingTask = ref<Task | null>(null)
const editOpen = ref(false)

function openEdit(task: Task) {
  editingTask.value = task
  editOpen.value = true
}

function contactLabel(task: Task): string {
  return task.contact_name || task.contact_phone
}

let reloadTimer: number | null = null
let unsubscribe: (() => void) | null = null

onMounted(() => {
  refresh()
  if (canReassign.value && usersStore.users.length === 0) {
    usersStore.fetchUsers().catch(() => { /* reassign menu stays empty */ })
  }
  // Another user changed a task: refetch (debounced) through our own permission scope
  unsubscribe = wsService.onTaskChanged(() => {
    if (reloadTimer) clearTimeout(reloadTimer)
    reloadTimer = window.setTimeout(refresh, 500)
  })
})

onUnmounted(() => {
  unsubscribe?.()
  if (reloadTimer) clearTimeout(reloadTimer)
})
</script>

<template>
  <div class="flex flex-col h-full bg-background light:bg-gray-50">
    <PageHeader
      :title="$t('tasks.pageTitle')"
      :description="$t('tasks.pageSubtitle')"
      :icon="ListTodo"
      icon-gradient="bg-gradient-to-br from-amber-500 to-orange-600 shadow-amber-500/20"
    />

    <div class="flex flex-wrap items-center gap-3 px-4 sm:px-6 py-3 border-b border-white/[0.08] light:border-gray-200">
      <div role="tablist" class="flex flex-wrap gap-1 rounded-lg bg-white/[0.04] light:bg-gray-100 p-1">
        <button
          v-for="b in buckets"
          :key="b"
          role="tab"
          :aria-selected="bucket === b"
          class="flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm transition-colors"
          :class="bucket === b
            ? 'bg-white/[0.08] text-white light:bg-white light:text-gray-900 shadow-sm'
            : 'text-white/50 hover:text-white/80 light:text-gray-500 light:hover:text-gray-800'"
          @click="bucket = b"
        >
          {{ $t(`tasks.bucket.${b}`) }}
          <span
            v-if="(b === 'overdue' || b === 'today') && counts[b] > 0"
            class="min-w-5 rounded-full px-1.5 text-xs tabular-nums"
            :class="b === 'overdue' ? 'bg-red-500/20 text-red-400 light:text-red-600' : 'bg-amber-500/20 text-amber-400 light:text-amber-700'"
          >{{ counts[b] }}</span>
        </button>
      </div>
      <Select v-model="owner">
        <SelectTrigger class="w-36">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="mine">{{ $t('tasks.ownerMine') }}</SelectItem>
          <SelectItem value="all">{{ $t('tasks.ownerAll') }}</SelectItem>
        </SelectContent>
      </Select>
      <Button v-if="canAskAlerts" variant="outline" size="sm" class="h-8" @click="enableAlerts">{{ $t('tasks.enableAlerts') }}</Button>
      <p class="hidden md:block ml-auto text-xs text-white/40 light:text-gray-500">{{ $t('tasks.createHint') }}</p>
    </div>

    <ErrorState
      v-if="error && !isLoading"
      :title="$t('common.loadErrorTitle')"
      :description="$t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="refresh"
    />

    <div v-else class="flex-1 overflow-y-auto p-4 sm:p-6">
      <div v-if="isLoading && !tasks.length" class="space-y-2">
        <Skeleton v-for="i in 5" :key="i" class="h-16 w-full bg-white/[0.08] light:bg-gray-200 rounded-xl" />
      </div>

      <div v-else-if="!tasks.length" class="py-16 text-center text-sm text-white/40 light:text-gray-500">
        {{ $t(`tasks.empty.${bucket}`) }}
      </div>

      <ul v-else class="space-y-2 max-w-4xl">
        <li
          v-for="task in tasks"
          :key="task.id"
          class="flex items-start gap-3 rounded-xl border border-white/[0.08] light:border-gray-200 bg-white/[0.02] light:bg-white p-3"
        >
          <Checkbox
            class="mt-0.5"
            :checked="task.done"
            :disabled="!canWrite || busyIds.has(task.id)"
            :aria-label="task.done ? $t('tasks.markOpen') : $t('tasks.markDone')"
            @update:checked="(v) => toggleDone(task, v === true)"
          />
          <div class="flex-1 min-w-0">
            <p
              class="text-sm font-medium"
              :class="task.done ? 'line-through text-white/40 light:text-gray-400' : 'text-white light:text-gray-900'"
            >{{ task.title }}</p>
            <p v-if="task.notes" class="mt-0.5 text-xs text-white/50 light:text-gray-500 whitespace-pre-line line-clamp-2">{{ task.notes }}</p>
            <div class="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-white/50 light:text-gray-500">
              <RouterLink
                :to="`/chat/${task.contact_id}`"
                class="flex items-center gap-1 hover:text-emerald-400 light:hover:text-emerald-600"
              >
                <MessageSquare class="h-3 w-3" />{{ contactLabel(task) }}
              </RouterLink>
              <Popover
                v-if="canWrite && !task.done"
                :open="rescheduleId === task.id"
                @update:open="(o) => openReschedule(task, o)"
              >
                <PopoverTrigger as-child>
                  <button
                    type="button"
                    class="flex items-center gap-1 rounded hover:text-white light:hover:text-gray-900"
                    :class="{ 'text-red-400 light:text-red-600 font-medium': task.overdue }"
                    :aria-label="$t('tasks.reschedule')"
                    :title="$t('tasks.reschedule')"
                  >
                    <template v-if="task.due_at">
                      <CalendarClock class="h-3 w-3" />{{ formatDateTime(task.due_at) }}
                    </template>
                    <template v-else>
                      <CalendarPlus class="h-3 w-3" />{{ $t('tasks.setDue') }}
                    </template>
                  </button>
                </PopoverTrigger>
                <PopoverContent class="w-72 space-y-3" align="start">
                  <p class="text-sm font-medium">{{ $t('tasks.reschedule') }}</p>
                  <div class="flex flex-wrap gap-1.5">
                    <Button
                      v-for="preset in DUE_PRESETS"
                      :key="preset.key"
                      variant="outline"
                      size="sm"
                      class="h-7 text-xs"
                      :disabled="busyIds.has(task.id)"
                      @click="reschedule(task, toLocalInput(preset.date()))"
                    >{{ $t(`tasks.preset.${preset.key}`) }}</Button>
                  </div>
                  <form class="flex items-center gap-2" @submit.prevent="reschedule(task, rescheduleValue)">
                    <Input v-model="rescheduleValue" type="datetime-local" class="h-8 text-xs" :aria-label="$t('tasks.due')" />
                    <Button type="submit" size="sm" class="h-8" :disabled="busyIds.has(task.id) || !rescheduleValue">{{ $t('common.save') }}</Button>
                  </form>
                  <Button
                    v-if="task.due_at"
                    variant="ghost"
                    size="sm"
                    class="h-7 px-2 text-xs"
                    :disabled="busyIds.has(task.id)"
                    @click="reschedule(task, '')"
                  >{{ $t('tasks.noDueDate') }}</Button>
                </PopoverContent>
              </Popover>
              <span
                v-else-if="task.due_at"
                class="flex items-center gap-1"
                :class="{ 'text-red-400 light:text-red-600 font-medium': task.overdue }"
              >
                <CalendarClock class="h-3 w-3" />{{ formatDateTime(task.due_at) }}
              </span>
              <Select
                v-if="canReassign && activeUsers.length && !task.done"
                :model-value="task.assignee_id ?? UNASSIGNED"
                :disabled="busyIds.has(task.id)"
                @update:model-value="(v) => reassign(task, v)"
              >
                <SelectTrigger class="h-6 w-auto gap-1 border-0 bg-transparent px-1 text-xs shadow-none" :aria-label="$t('tasks.assignee')">
                  <User class="h-3 w-3" />
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem :value="UNASSIGNED">{{ $t('tasks.unassigned') }}</SelectItem>
                  <SelectItem v-for="u in activeUsers" :key="u.id" :value="u.id">{{ u.full_name || u.email }}</SelectItem>
                  <SelectItem
                    v-if="task.assignee_id && !activeUsers.some(u => u.id === task.assignee_id)"
                    :value="task.assignee_id"
                  >{{ task.assignee_name || task.assignee_id }}</SelectItem>
                </SelectContent>
              </Select>
              <span v-else-if="owner === 'all'" class="flex items-center gap-1">
                <User class="h-3 w-3" />{{ task.assignee_name || $t('tasks.unassigned') }}
              </span>
            </div>
          </div>
          <Button
            v-if="canWrite && !task.done"
            variant="ghost"
            size="icon"
            class="h-8 w-8 shrink-0"
            :disabled="busyIds.has(task.id)"
            :aria-label="$t('tasks.editFollowUp')"
            @click="openEdit(task)"
          >
            <Pencil class="h-4 w-4" />
          </Button>
          <Button
            v-if="canWrite"
            variant="ghost"
            size="icon"
            class="h-8 w-8 shrink-0"
            :disabled="busyIds.has(task.id)"
            :aria-label="$t('tasks.delete')"
            @click="removeTask(task)"
          >
            <Trash2 class="h-4 w-4" />
          </Button>
        </li>
        <li v-if="total > tasks.length" class="pt-2 text-center text-xs text-white/40 light:text-gray-500">
          {{ $t('tasks.showingFirst', { shown: tasks.length, total }) }}
        </li>
      </ul>
    </div>

    <FollowUpDialog
      v-if="editingTask"
      v-model:open="editOpen"
      :contact-id="editingTask.contact_id"
      :contact-name="contactLabel(editingTask)"
      :task="editingTask"
      @saved="refresh"
    />
  </div>
</template>
