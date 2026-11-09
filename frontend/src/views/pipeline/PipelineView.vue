<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { TagBadge } from '@/components/ui/tag-badge'
import { PageHeader, SearchInput, ErrorState } from '@/components/shared'
import PipelineEditorDialog from '@/views/pipeline/PipelineEditorDialog.vue'
import FollowUpDialog from '@/components/tasks/FollowUpDialog.vue'
import LeadFieldsDialog from '@/views/pipeline/LeadFieldsDialog.vue'
import NoReplyRuleDialog from '@/views/pipeline/NoReplyRuleDialog.vue'
import LeadQueueDialog from '@/views/pipeline/LeadQueueDialog.vue'
import DuplicatesDialog from '@/views/pipeline/DuplicatesDialog.vue'
import LeadSourcesDialog from '@/views/pipeline/LeadSourcesDialog.vue'
import BulkTemplateDialog from '@/views/pipeline/BulkTemplateDialog.vue'
import SalesSettingsDialog from '@/views/pipeline/SalesSettingsDialog.vue'
import { Checkbox } from '@/components/ui/checkbox'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { KanbanSquare, Settings2, Clock, User, CalendarPlus, CalendarClock, ClipboardList, TimerReset, Filter, Inbox, Users2, Plug, BarChart3, X, MoreHorizontal, UsersRound, Workflow, Phone, MessageCircleReply, Send, SlidersHorizontal } from 'lucide-vue-next'
import { usePipelinesStore } from '@/stores/pipelines'
import { useAuthStore } from '@/stores/auth'
import { useUsersStore } from '@/stores/users'
import { useLeadFieldsStore } from '@/stores/leadFields'
import { leadsService, pipelinesService, type BoardCard, type BoardColumn, type BoardParams, type PipelineBoard, type PipelineStats } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'
import { debounce } from '@/lib/utils'
import { getStageDotClass } from '@/lib/constants'
import { wsService } from '@/services/websocket'
import { adjacentStageId, positionFor, telHref, windowLeft } from '@/lib/pipelineBoard'

const { t } = useI18n()
const router = useRouter()
const pipelinesStore = usePipelinesStore()
const authStore = useAuthStore()
const usersStore = useUsersStore()
const leadFieldsStore = useLeadFieldsStore()

const selectedPipelineId = ref<string>('')
const board = ref<PipelineBoard | null>(null)
const isLoading = ref(false)
const error = ref(false)
const searchQuery = ref('')
const ownerFilter = ref<'all' | 'mine' | 'unassigned'>('all')
const editorOpen = ref(false)
const editorPipelineId = ref<string | null>(null)
const followUpOpen = ref(false)
const leadFieldsOpen = ref(false)
const automationOpen = ref(false)
const followUpCard = ref<BoardCard | null>(null)

// Drag state
const draggingCard = ref<BoardCard | null>(null)
const dragSourceStageId = ref<string | null>(null)
const dropTarget = ref<{ stageId: string; index: number } | null>(null)

const canManage = computed(() => authStore.hasPermission('settings.general', 'write'))
const canMove = computed(() => authStore.hasPermission('chat', 'write'))

async function loadPipelines() {
  try {
    await pipelinesStore.fetchPipelines(true)
    const ids = pipelinesStore.pipelines.map(p => p.id)
    if (!ids.includes(selectedPipelineId.value)) {
      selectedPipelineId.value = pipelinesStore.defaultPipeline?.id ?? ''
    }
  } catch (e) {
    toast.error(getErrorMessage(e, t('pipeline.failedLoad')))
    error.value = true
  }
}

const queueOpen = ref(false)
const duplicatesOpen = ref(false)
const sourcesOpen = ref(false)
const canCleanUp = computed(() => authStore.hasPermission('contacts', 'write'))
// Team managers without contacts:read learn access from the report itself
const teamAccess = ref(false)
const canSeeTeam = computed(() => authStore.hasPermission('contacts', 'read') || teamAccess.value)
const salesSettingsOpen = ref(false)
const bulkTemplateOpen = ref(false)
// The DropdownMenu wrapper is controlled: it stays shut without v-model
const moreOpen = ref(false)

const tagFilter = ref('')
const leadKeyFilter = ref('')
const leadValueFilter = ref('')
const createdFrom = ref('')
const createdTo = ref('')
const activeFilterCount = computed(() =>
  [tagFilter.value, leadKeyFilter.value && leadValueFilter.value, createdFrom.value, createdTo.value].filter(Boolean).length
)

function clearFilters() {
  tagFilter.value = leadKeyFilter.value = leadValueFilter.value = createdFrom.value = createdTo.value = ''
}

function boardFilters(): BoardParams {
  const params: BoardParams = {}
  if (searchQuery.value) params.search = searchQuery.value
  if (tagFilter.value) params.tag = tagFilter.value.trim()
  if (leadKeyFilter.value && leadValueFilter.value) {
    params.lead_key = leadKeyFilter.value
    params.lead_value = leadValueFilter.value.trim()
  }
  if (createdFrom.value) params.created_after = new Date(`${createdFrom.value}T00:00:00`).toISOString()
  if (createdTo.value) {
    const end = new Date(`${createdTo.value}T00:00:00`)
    end.setDate(end.getDate() + 1)
    params.created_before = end.toISOString()
  }
  if (ownerFilter.value === 'mine' && authStore.user?.id) params.assigned_user_id = authStore.user.id
  if (ownerFilter.value === 'unassigned') params.assigned_user_id = 'unassigned'
  return params
}

const loadingMore = ref<string | null>(null)

// Append the next page of one column's cards
async function loadMore(column: BoardColumn) {
  if (!board.value || loadingMore.value) return
  loadingMore.value = column.id
  try {
    const response = await pipelinesService.board(selectedPipelineId.value, {
      ...boardFilters(),
      stage_id: column.id,
      offset: column.cards.length
    })
    const page = unwrapResponse<PipelineBoard>(response).columns[0]
    if (page) {
      const seen = new Set(column.cards.map(c => c.id))
      column.cards.push(...page.cards.filter(c => !seen.has(c.id)))
      column.total = page.total
    }
  } catch (e) {
    toast.error(getErrorMessage(e, t('pipeline.failedLoad')))
  } finally {
    loadingMore.value = null
  }
}

async function loadBoard() {
  if (!selectedPipelineId.value) return
  isLoading.value = true
  error.value = false
  try {
    const response = await pipelinesService.board(selectedPipelineId.value, boardFilters())
    board.value = unwrapResponse<PipelineBoard>(response)
  } catch (e) {
    toast.error(getErrorMessage(e, t('pipeline.failedLoad')))
    error.value = true
  } finally {
    isLoading.value = false
  }
}

const stats = ref<PipelineStats | null>(null)

async function loadStats() {
  if (!selectedPipelineId.value) return
  const params: { assigned_user_id?: string } = {}
  if (ownerFilter.value === 'mine') params.assigned_user_id = 'me'
  if (ownerFilter.value === 'unassigned') params.assigned_user_id = 'unassigned'
  try {
    const response = await pipelinesService.stats(selectedPipelineId.value, params)
    stats.value = unwrapResponse<PipelineStats>(response)
  } catch {
    stats.value = null // the board still works without numbers
  }
}

const winRateLabel = computed(() =>
  stats.value?.win_rate == null ? '—' : `${Math.round(stats.value.win_rate * 100)}%`
)

const debouncedLoadStats = debounce(loadStats, 1000)

const debouncedLoadBoard = debounce(loadBoard, 300)

watch(selectedPipelineId, () => { loadBoard(); loadStats() })
watch(ownerFilter, () => { loadBoard(); loadStats() })
watch([searchQuery, tagFilter, leadKeyFilter, leadValueFilter, createdFrom, createdTo], () => debouncedLoadBoard())

// --- Card helpers ---

function daysInStage(card: BoardCard): string {
  if (!card.stage_entered_at) return ''
  const days = Math.floor((Date.now() - new Date(card.stage_entered_at).getTime()) / 86_400_000)
  return days <= 0 ? t('pipeline.today') : t('pipeline.days', { n: days })
}

function cardName(card: BoardCard): string {
  return card.profile_name || card.phone_number
}

function openFollowUp(card: BoardCard) {
  followUpCard.value = card
  followUpOpen.value = true
}

function onFollowUpCreated() {
  const card = followUpCard.value
  if (!card) return
  card.open_tasks++
  // Server recomputes next due on the next board load; keep the badge sensible now
  loadBoard()
}

function taskBadge(card: BoardCard): { label: string; overdue: boolean } | null {
  if (!card.open_tasks) return null
  if (!card.next_task_due_at) return { label: t('pipeline.tasks', { n: card.open_tasks }), overdue: false }
  const due = new Date(card.next_task_due_at)
  const overdue = due.getTime() < Date.now()
  const sameDay = due.toDateString() === new Date().toDateString()
  const label = sameDay
    ? due.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : due.toLocaleDateString([], { day: 'numeric', month: 'short' })
  return { label, overdue }
}

function openChat(card: BoardCard) {
  router.push(`/chat/${card.id}`)
}

function stageAccentClass(color: string): string {
  return getStageDotClass(color || 'gray')
}

// --- Drag and drop (native HTML5) ---

function onDragStart(event: DragEvent, card: BoardCard, column: BoardColumn) {
  if (!canMove.value) return
  draggingCard.value = card
  dragSourceStageId.value = column.id
  event.dataTransfer?.setData('text/plain', card.id)
  if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move'
}

function onDragEnd() {
  if (pendingRemoteReload) {
    pendingRemoteReload = false
    debouncedLoadBoard()
    debouncedLoadStats()
  }
  draggingCard.value = null
  dragSourceStageId.value = null
  dropTarget.value = null
}

// Work out the insert index from the pointer position over the column's cards.
function onColumnDragOver(event: DragEvent, column: BoardColumn) {
  if (!draggingCard.value) return
  event.preventDefault()
  const list = (event.currentTarget as HTMLElement).querySelector('[data-card-list]')
  const cardEls = list ? Array.from(list.querySelectorAll<HTMLElement>('[data-card-id]')) : []
  let index = cardEls.length
  for (let i = 0; i < cardEls.length; i++) {
    const rect = cardEls[i].getBoundingClientRect()
    if (event.clientY < rect.top + rect.height / 2) {
      index = i
      break
    }
  }
  dropTarget.value = { stageId: column.id, index }
}

function onColumnDragLeave(event: DragEvent, column: BoardColumn) {
  const related = event.relatedTarget as Node | null
  if (related && (event.currentTarget as HTMLElement).contains(related)) return
  if (dropTarget.value?.stageId === column.id) dropTarget.value = null
}

async function onColumnDrop(event: DragEvent, column: BoardColumn) {
  event.preventDefault()
  const card = draggingCard.value
  const target = dropTarget.value
  const sourceStageId = dragSourceStageId.value
  onDragEnd()
  if (!card || !target || !board.value) return

  const source = board.value.columns.find(c => c.id === sourceStageId)
  if (!source) return

  const sourceIndex = source.cards.findIndex(c => c.id === card.id)
  const targetCards = column.cards.filter(c => c.id !== card.id)
  let insertIndex = target.index
  // Index was computed with the dragged card still in the list
  if (source.id === column.id && sourceIndex > -1 && sourceIndex < target.index) insertIndex--
  if (source.id === column.id && insertIndex === sourceIndex) return

  const position = positionFor(targetCards, insertIndex, column.total, source.id === column.id)

  // Optimistic update
  const snapshot = JSON.parse(JSON.stringify(board.value.columns)) as BoardColumn[]
  if (sourceIndex > -1) source.cards.splice(sourceIndex, 1)
  const moved: BoardCard = { ...card, stage_id: column.id }
  if (position !== undefined) moved.pipeline_position = position
  if (source.id !== column.id) {
    source.total--
    column.total++
    moved.stage_entered_at = new Date().toISOString()
  }
  column.cards = [...targetCards.slice(0, insertIndex), moved, ...targetCards.slice(insertIndex)]

  try {
    const updated = await pipelinesStore.moveContact(card.id, column.id, position)
    const idx = column.cards.findIndex(c => c.id === card.id)
    if (idx > -1) column.cards[idx] = updated
    if (source.id !== column.id) debouncedLoadStats()
  } catch (e) {
    board.value.columns = snapshot
    toast.error(getErrorMessage(e, t('pipeline.failedMove')))
  }
}

// --- Moves without dragging (keyboard and phone) ---

const liveMessage = ref('')

async function moveToStage(card: BoardCard, source: BoardColumn, targetId: string) {
  if (!board.value || targetId === source.id || !canMove.value) return
  const target = board.value.columns.find(c => c.id === targetId)
  if (!target) return
  const snapshot = JSON.parse(JSON.stringify(board.value.columns)) as BoardColumn[]
  source.cards = source.cards.filter(c => c.id !== card.id)
  source.total--
  target.total++
  target.cards.push({ ...card, stage_id: target.id })
  try {
    const updated = await pipelinesStore.moveContact(card.id, target.id)
    const idx = target.cards.findIndex(c => c.id === card.id)
    if (idx > -1) target.cards[idx] = updated
    liveMessage.value = t('pipeline.movedTo', { name: cardName(card), stage: target.name })
    debouncedLoadStats()
  } catch (e) {
    board.value.columns = snapshot
    toast.error(getErrorMessage(e, t('pipeline.failedMove')))
  }
}

function onCardArrow(card: BoardCard, column: BoardColumn, direction: -1 | 1) {
  if (!board.value) return
  const target = adjacentStageId(board.value.columns.map(c => c.id), column.id, direction)
  if (target) moveToStage(card, column, target)
}

// --- Selection and bulk actions ---

const selectedIds = ref(new Set<string>())
const bulkStage = ref('')
const bulkOwner = ref('')
const bulkTag = ref('')
const isBulking = ref(false)
const canBulkAssign = computed(() =>
  authStore.hasPermission('contacts', 'write') && authStore.hasPermission('users', 'read')
)
const activeUsers = computed(() => usersStore.users.filter(u => u.is_active))

function toggleSelected(card: BoardCard, value: boolean) {
  const next = new Set(selectedIds.value)
  if (value) next.add(card.id)
  else next.delete(card.id)
  selectedIds.value = next
  if (next.size && canBulkAssign.value && !usersStore.users.length) {
    usersStore.fetchUsers().catch(() => { /* owner picker stays empty */ })
  }
}

function clearSelection() {
  selectedIds.value = new Set()
  bulkStage.value = bulkOwner.value = bulkTag.value = ''
}

async function applyBulk() {
  const tag = bulkTag.value.trim()
  if (!bulkStage.value && !bulkOwner.value && !tag) return
  isBulking.value = true
  try {
    const response = await leadsService.bulk({
      contact_ids: [...selectedIds.value],
      stage_id: bulkStage.value || undefined,
      assigned_user_id: bulkOwner.value || undefined,
      add_tags: tag ? [tag] : undefined
    })
    const { updated } = unwrapResponse<{ updated: number }>(response)
    toast.success(t('pipeline.bulkDone', { n: updated }))
    clearSelection()
    await loadBoard()
    loadStats()
  } catch (e) {
    toast.error(getErrorMessage(e, t('pipeline.bulkFailed')))
  } finally {
    isBulking.value = false
  }
}

function refreshAll() {
  loadBoard()
  loadStats()
}

// --- Live updates from other users ---

// Another user (or the lead API) moved a contact. The event carries ids only,
// so reload the board through this user's own permission scope.
interface StageChangedEvent {
  contact_id: string
  pipeline_id?: string | null
  stage_id?: string | null
}

let pendingRemoteReload = false

function applyRemoteMove(event: StageChangedEvent) {
  if (!board.value) return
  const shown = board.value.columns.flatMap(c => c.cards).find(c => c.id === event.contact_id)
  // Our own move: the card is already where the server says
  if (shown && shown.stage_id === event.stage_id) return
  if (!shown && event.pipeline_id !== board.value.pipeline.id) return
  // Don't pull the board out from under an active drag; reload when it ends
  if (draggingCard.value) {
    pendingRemoteReload = true
    return
  }
  debouncedLoadBoard()
  debouncedLoadStats()
}

let unsubscribe: (() => void) | null = null

onMounted(async () => {
  unsubscribe = wsService.onContactStageChanged(applyRemoteMove)
  leadFieldsStore.fetchFields().catch(() => { /* field filter stays empty */ })
  if (!authStore.hasPermission('contacts', 'read')) {
    pipelinesService.teamReport().then(() => { teamAccess.value = true }).catch(() => { /* not a team manager */ })
  }
  await loadPipelines()
  if (selectedPipelineId.value && !board.value) {
    await loadBoard()
    loadStats()
  }
})

onUnmounted(() => unsubscribe?.())

// --- Pipeline editor ---

function openEditor(pipelineId: string | null) {
  editorPipelineId.value = pipelineId
  editorOpen.value = true
}

async function onEditorSaved(pipelineId: string | null) {
  await loadPipelines()
  if (pipelineId) selectedPipelineId.value = pipelineId
  await loadBoard()
}
</script>

<template>
  <div class="flex flex-col h-full bg-background light:bg-gray-50">
    <PageHeader
      :title="$t('pipeline.title')"
      :description="$t('pipeline.subtitle')"
      :icon="KanbanSquare"
      icon-gradient="bg-gradient-to-br from-emerald-600 to-teal-700 shadow-emerald-500/20"
    >
      <template #actions>
        <Button v-if="canMove" variant="outline" size="sm" class="mr-2" @click="queueOpen = true">
          <Inbox class="h-4 w-4 sm:mr-2" /><span class="hidden sm:inline">{{ $t('queue.title') }}</span>
          <span class="sr-only sm:hidden">{{ $t('queue.title') }}</span>
        </Button>
        <DropdownMenu v-model:open="moreOpen">
          <DropdownMenuTrigger as-child>
            <Button variant="outline" size="sm" :aria-label="$t('pipeline.more')">
              <MoreHorizontal class="h-4 w-4 sm:mr-2" /><span class="hidden sm:inline">{{ $t('pipeline.more') }}</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" class="w-52">
            <DropdownMenuItem @click="router.push('/pipeline/reports')"><BarChart3 class="h-4 w-4 mr-2" />{{ $t('reports.title') }}</DropdownMenuItem>
            <DropdownMenuItem v-if="canSeeTeam" @click="router.push('/pipeline/team')"><UsersRound class="h-4 w-4 mr-2" />{{ $t('team.title') }}</DropdownMenuItem>
            <DropdownMenuItem v-if="canManage" @click="router.push('/pipeline/automations')"><Workflow class="h-4 w-4 mr-2" />{{ $t('rules.title') }}</DropdownMenuItem>
            <DropdownMenuSeparator v-if="canManage || canCleanUp" />
            <DropdownMenuItem v-if="canCleanUp" @click="duplicatesOpen = true"><Users2 class="h-4 w-4 mr-2" />{{ $t('duplicates.title') }}</DropdownMenuItem>
            <DropdownMenuItem v-if="canManage" @click="salesSettingsOpen = true"><SlidersHorizontal class="h-4 w-4 mr-2" />{{ $t('sales.title') }}</DropdownMenuItem>
            <DropdownMenuItem v-if="canManage" @click="sourcesOpen = true"><Plug class="h-4 w-4 mr-2" />{{ $t('sources.title') }}</DropdownMenuItem>
            <DropdownMenuItem v-if="canManage" @click="automationOpen = true"><TimerReset class="h-4 w-4 mr-2" />{{ $t('automation.button') }}</DropdownMenuItem>
            <DropdownMenuItem v-if="canManage" @click="leadFieldsOpen = true"><ClipboardList class="h-4 w-4 mr-2" />{{ $t('lead.editFields') }}</DropdownMenuItem>
            <DropdownMenuItem v-if="canManage" @click="openEditor(selectedPipelineId || null)"><Settings2 class="h-4 w-4 mr-2" />{{ $t('pipeline.editStages') }}</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </template>
    </PageHeader>

    <!-- Toolbar -->
    <div class="flex flex-wrap items-center gap-2 sm:gap-3 px-4 sm:px-6 py-3 border-b border-white/[0.08] light:border-gray-200">
      <Select v-if="pipelinesStore.pipelines.length > 1" v-model="selectedPipelineId">
        <SelectTrigger class="w-48">
          <SelectValue :placeholder="$t('pipeline.selectPipeline')" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem v-for="p in pipelinesStore.pipelines" :key="p.id" :value="p.id">{{ p.name }}</SelectItem>
        </SelectContent>
      </Select>
      <SearchInput v-model="searchQuery" :placeholder="$t('pipeline.searchPlaceholder')" class="w-full sm:w-64" />
      <Select v-model="ownerFilter">
        <SelectTrigger class="w-40">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">{{ $t('pipeline.ownerAll') }}</SelectItem>
          <SelectItem value="mine">{{ $t('pipeline.ownerMine') }}</SelectItem>
          <SelectItem value="unassigned">{{ $t('pipeline.ownerUnassigned') }}</SelectItem>
        </SelectContent>
      </Select>
      <Popover>
        <PopoverTrigger as-child>
          <Button variant="outline" size="sm">
            <Filter class="h-4 w-4 mr-2" />{{ $t('pipeline.filters') }}
            <span v-if="activeFilterCount" class="ml-1.5 rounded-full bg-emerald-500/20 px-1.5 text-xs tabular-nums">{{ activeFilterCount }}</span>
          </Button>
        </PopoverTrigger>
        <PopoverContent class="w-72 space-y-3" align="start">
          <div class="space-y-1">
            <label class="text-xs font-medium" for="filter-tag">{{ $t('pipeline.filterTag') }}</label>
            <Input id="filter-tag" v-model="tagFilter" class="h-8 text-sm" />
          </div>
          <div class="space-y-1">
            <label class="text-xs font-medium" for="filter-field">{{ $t('pipeline.filterField') }}</label>
            <div class="flex gap-2">
              <select id="filter-field" v-model="leadKeyFilter" class="h-8 w-28 rounded-md border bg-background text-foreground px-1 text-sm">
                <option value="">—</option>
                <option v-for="f in leadFieldsStore.fields" :key="f.key" :value="f.key">{{ f.label }}</option>
              </select>
              <Input v-model="leadValueFilter" class="h-8 text-sm" :disabled="!leadKeyFilter" :aria-label="$t('pipeline.filterValue')" :placeholder="$t('pipeline.filterValue')" />
            </div>
          </div>
          <div class="grid grid-cols-2 gap-2">
            <div class="space-y-1">
              <label class="text-xs font-medium" for="filter-from">{{ $t('pipeline.createdFrom') }}</label>
              <Input id="filter-from" v-model="createdFrom" type="date" class="h-8 text-xs" />
            </div>
            <div class="space-y-1">
              <label class="text-xs font-medium" for="filter-to">{{ $t('pipeline.createdTo') }}</label>
              <Input id="filter-to" v-model="createdTo" type="date" class="h-8 text-xs" />
            </div>
          </div>
          <Button variant="ghost" size="sm" class="h-7 px-2 text-xs" :disabled="!activeFilterCount" @click="clearFilters">{{ $t('pipeline.clearFilters') }}</Button>
        </PopoverContent>
      </Popover>
      <p class="hidden md:block text-xs text-white/40 light:text-gray-500 ml-auto">{{ $t('pipeline.keyboardHint') }}</p>
    </div>

    <p class="sr-only" aria-live="polite">{{ liveMessage }}</p>

    <!-- Bulk actions -->
    <div
      v-if="selectedIds.size"
      class="flex flex-wrap items-center gap-2 px-4 sm:px-6 py-2 border-b border-white/[0.08] light:border-gray-200 bg-emerald-500/[0.06]"
    >
      <span class="text-sm font-medium text-white light:text-gray-900">{{ $t('pipeline.selected', { n: selectedIds.size }) }}</span>
      <select v-model="bulkStage" class="h-9 rounded-md border bg-background text-foreground px-2 text-sm" :aria-label="$t('pipeline.bulkMove')">
        <option value="">{{ $t('pipeline.bulkMove') }}</option>
        <option v-for="column in board?.columns ?? []" :key="column.id" :value="column.id">{{ column.name }}</option>
      </select>
      <select v-if="canBulkAssign" v-model="bulkOwner" class="h-9 rounded-md border bg-background text-foreground px-2 text-sm" :aria-label="$t('pipeline.bulkAssign')">
        <option value="">{{ $t('pipeline.bulkAssign') }}</option>
        <option v-for="u in activeUsers" :key="u.id" :value="u.id">{{ u.full_name || u.email }}</option>
      </select>
      <Input v-model="bulkTag" class="h-9 w-36 text-sm" maxlength="50" :placeholder="$t('pipeline.bulkTag')" :aria-label="$t('pipeline.bulkTag')" />
      <Button v-if="canCleanUp" variant="outline" size="sm" class="h-9" @click="bulkTemplateOpen = true">
        <Send class="h-4 w-4 mr-1.5" />{{ $t('bulkTemplate.title') }}
      </Button>
      <Button size="sm" class="h-9" :disabled="isBulking || (!bulkStage && !bulkOwner && !bulkTag.trim())" @click="applyBulk">{{ $t('pipeline.bulkApply') }}</Button>
      <Button variant="ghost" size="sm" class="h-9" @click="clearSelection">
        <X class="h-4 w-4 mr-1" />{{ $t('pipeline.clearSelection') }}
      </Button>
    </div>

    <!-- Conversion numbers -->
    <div
      v-if="stats"
      class="flex flex-wrap items-center gap-x-4 sm:gap-x-6 gap-y-1.5 px-4 sm:px-6 py-2.5 border-b border-white/[0.08] light:border-gray-200 text-sm"
    >
      <span class="text-white/50 light:text-gray-500">{{ $t('pipeline.stats.total') }}
        <strong class="ml-1 tabular-nums text-white light:text-gray-900">{{ stats.total }}</strong></span>
      <span class="text-white/50 light:text-gray-500">{{ $t('pipeline.stats.open') }}
        <strong class="ml-1 tabular-nums text-white light:text-gray-900">{{ stats.open }}</strong></span>
      <span class="text-white/50 light:text-gray-500">{{ $t('pipeline.kind.won') }}
        <strong class="ml-1 tabular-nums text-green-400 light:text-green-700">{{ stats.won }}</strong></span>
      <span class="text-white/50 light:text-gray-500">{{ $t('pipeline.kind.lost') }}
        <strong class="ml-1 tabular-nums text-white light:text-gray-900">{{ stats.lost }}</strong></span>
      <span class="text-white/50 light:text-gray-500" :title="$t('pipeline.stats.winRateHint')">{{ $t('pipeline.stats.winRate') }}
        <strong class="ml-1 tabular-nums text-white light:text-gray-900">{{ winRateLabel }}</strong></span>
      <RouterLink
        v-if="stats.overdue_tasks"
        to="/tasks"
        class="text-red-400 light:text-red-600 hover:underline"
      >{{ $t('pipeline.stats.overdueTasks', { n: stats.overdue_tasks }) }}</RouterLink>
      <span v-if="stats.stale_open_leads" class="text-amber-400 light:text-amber-700" :title="$t('pipeline.stats.staleHint')">
        {{ $t('pipeline.stats.stale', { n: stats.stale_open_leads }) }}
      </span>
      <span v-if="stats.by_source.length" class="flex flex-wrap items-center gap-1.5 ml-auto text-xs">
        <span class="text-white/40 light:text-gray-500">{{ $t('pipeline.stats.bySource') }}</span>
        <span
          v-for="s in stats.by_source.slice(0, 5)"
          :key="s.source"
          class="rounded-full bg-white/[0.06] light:bg-gray-100 px-2 py-0.5 text-white/70 light:text-gray-700"
          :title="$t('pipeline.stats.sourceHint', { won: s.won })"
        >{{ s.source || $t('pipeline.stats.noSource') }} <span class="tabular-nums">{{ s.count }}</span></span>
      </span>
    </div>

    <ErrorState
      v-if="error && !isLoading"
      :title="$t('common.loadErrorTitle')"
      :description="$t('common.loadErrorDescription')"
      :retry-label="$t('common.retryLoad')"
      class="flex-1"
      @retry="loadBoard"
    />

    <!-- Board -->
    <div v-else class="flex-1 overflow-x-auto overflow-y-hidden">
      <div v-if="isLoading && !board" class="flex gap-4 p-6">
        <Skeleton v-for="i in 5" :key="i" class="h-96 w-72 shrink-0 bg-white/[0.08] light:bg-gray-200 rounded-xl" />
      </div>

      <div v-else-if="board" class="flex gap-4 p-4 sm:p-6 h-full items-start snap-x snap-mandatory sm:snap-none">
        <section
          v-for="column in board.columns"
          :key="column.id"
          class="w-[85vw] sm:w-72 shrink-0 snap-center flex flex-col max-h-full rounded-xl border bg-white/[0.02] light:bg-white transition-colors"
          :class="dropTarget?.stageId === column.id
            ? 'border-emerald-500/60 light:border-emerald-400'
            : 'border-white/[0.08] light:border-gray-200'"
          :aria-label="column.name"
          @dragover="onColumnDragOver($event, column)"
          @dragleave="onColumnDragLeave($event, column)"
          @drop="onColumnDrop($event, column)"
        >
          <header class="flex items-center gap-2 px-3 py-2.5 border-b border-white/[0.06] light:border-gray-100">
            <span :class="['h-2.5 w-2.5 rounded-full', stageAccentClass(column.color)]" />
            <h2 class="text-sm font-medium text-white light:text-gray-900 truncate">{{ column.name }}</h2>
            <span
              v-if="column.kind !== 'open'"
              class="text-[10px] uppercase tracking-wide text-white/40 light:text-gray-400"
            >{{ $t(`pipeline.kind.${column.kind}`) }}</span>
            <span v-if="column.value" class="ml-auto text-xs tabular-nums text-emerald-400 light:text-emerald-700" :title="$t('pipeline.stageValue')">{{ column.value.toLocaleString() }}</span>
            <span class="text-xs tabular-nums text-white/50 light:text-gray-500" :class="{ 'ml-auto': !column.value }">{{ column.total }}</span>
          </header>

          <div data-card-list class="flex-1 overflow-y-auto p-2 space-y-2 min-h-[4rem]">
            <template v-for="(card, index) in column.cards" :key="card.id">
              <div
                v-if="dropTarget?.stageId === column.id && dropTarget?.index === index"
                class="h-1 rounded bg-emerald-500/70"
              />
              <article
                :data-card-id="card.id"
                :draggable="canMove"
                class="group rounded-lg border border-white/[0.08] light:border-gray-200 bg-card light:bg-white p-3 cursor-pointer hover:border-white/20 light:hover:border-gray-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-500"
                :class="{ 'opacity-40': draggingCard?.id === card.id }"
                tabindex="0"
                @dragstart="onDragStart($event, card, column)"
                @dragend="onDragEnd"
                @click="openChat(card)"
                @keydown.enter="openChat(card)"
                @keydown.alt.left.prevent="onCardArrow(card, column, -1)"
                @keydown.alt.right.prevent="onCardArrow(card, column, 1)"
              >
                <div class="flex items-start gap-2">
                  <Checkbox
                    v-if="canMove"
                    class="mt-0.5 shrink-0"
                    :class="selectedIds.size ? '' : 'sm:-mr-2 sm:w-0 sm:opacity-0 sm:group-hover:mr-0 sm:group-hover:w-4 sm:group-hover:opacity-100 sm:group-focus-within:mr-0 sm:group-focus-within:w-4 sm:group-focus-within:opacity-100'"
                    :checked="selectedIds.has(card.id)"
                    :aria-label="$t('pipeline.selectCard', { name: cardName(card) })"
                    @click.stop
                    @keydown.enter.stop
                    @update:checked="(v) => toggleSelected(card, v === true)"
                  />
                  <p class="flex-1 min-w-0 text-sm font-medium text-white light:text-gray-900 truncate">{{ cardName(card) }}</p>
                  <span
                    class="mt-1 h-2 w-2 shrink-0 rounded-full"
                    :class="card.service_window_open ? 'bg-emerald-500' : 'bg-white/20 light:bg-gray-300'"
                    :title="card.service_window_open ? $t('pipeline.windowOpen') : $t('pipeline.windowClosed')"
                  />
                </div>
                <p v-if="card.profile_name" class="text-xs text-white/40 light:text-gray-500 truncate">{{ card.phone_number }}</p>
                <p v-if="card.last_message_preview" class="mt-1.5 text-xs text-white/60 light:text-gray-600 line-clamp-2">
                  {{ card.last_message_preview }}
                </p>
                <p
                  v-if="card.awaiting_reply && windowLeft(card.window_expires_at)"
                  class="mt-1.5 flex items-center gap-1 text-xs font-medium"
                  :class="windowLeft(card.window_expires_at)!.urgent ? 'text-red-400 light:text-red-600' : 'text-amber-400 light:text-amber-700'"
                  :title="$t('pipeline.replyHint')"
                >
                  <MessageCircleReply class="h-3 w-3 shrink-0" />{{ $t('pipeline.replyLeft', { time: windowLeft(card.window_expires_at)!.label }) }}
                </p>
                <p v-if="card.deal_value" class="mt-1.5 text-xs font-medium tabular-nums text-emerald-400 light:text-emerald-700">{{ card.deal_value.toLocaleString() }}</p>
                <div v-if="card.tags.length" class="mt-2 flex flex-wrap gap-1">
                  <TagBadge v-for="tag in card.tags.slice(0, 3)" :key="tag">{{ tag }}</TagBadge>
                  <span v-if="card.tags.length > 3" class="text-xs text-white/40 light:text-gray-400">+{{ card.tags.length - 3 }}</span>
                </div>
                <div class="mt-2 flex items-center gap-3 text-[11px] text-white/40 light:text-gray-500">
                  <span class="flex items-center gap-1 min-w-0">
                    <User class="h-3 w-3 shrink-0" />
                    <span class="truncate">{{ card.assigned_user_name || $t('pipeline.unassigned') }}</span>
                  </span>
                  <span
                    v-if="taskBadge(card)"
                    class="flex items-center gap-1 shrink-0"
                    :class="taskBadge(card)!.overdue ? 'text-red-400 light:text-red-600 font-medium' : ''"
                    :title="$t('pipeline.nextFollowUp')"
                  >
                    <CalendarClock class="h-3 w-3" />{{ taskBadge(card)!.label }}
                  </span>
                  <a
                    v-if="telHref(card.phone_number)"
                    :href="telHref(card.phone_number)!"
                    class="shrink-0 rounded p-2 -m-1.5 sm:p-0.5 sm:m-0 sm:opacity-0 sm:group-hover:opacity-100 focus-visible:opacity-100 hover:text-emerald-400 light:hover:text-emerald-600"
                    :aria-label="$t('pipeline.callLead', { name: cardName(card) })"
                    :title="$t('pipeline.callLead', { name: cardName(card) })"
                    @click.stop
                    @keydown.enter.stop
                  >
                    <Phone class="h-3.5 w-3.5" />
                  </a>
                  <button
                    v-if="canMove"
                    type="button"
                    class="shrink-0 rounded p-2 -m-1.5 sm:p-0.5 sm:m-0 sm:opacity-0 sm:group-hover:opacity-100 focus-visible:opacity-100 hover:text-emerald-400 light:hover:text-emerald-600"
                    :aria-label="$t('tasks.newFollowUp')"
                    :title="$t('tasks.newFollowUp')"
                    @click.stop="openFollowUp(card)"
                    @keydown.enter.stop
                  >
                    <CalendarPlus class="h-3.5 w-3.5" />
                  </button>
                  <span v-if="card.stage_entered_at" class="flex items-center gap-1 ml-auto shrink-0">
                    <Clock class="h-3 w-3" />{{ daysInStage(card) }}
                  </span>
                </div>
                <select
                  v-if="canMove"
                  class="sm:hidden mt-2 ml-auto block h-9 w-36 rounded-md border bg-background text-foreground px-2 text-xs"
                  :value="column.id"
                  :aria-label="$t('pipeline.moveTo')"
                  @click.stop
                  @change="moveToStage(card, column, ($event.target as HTMLSelectElement).value)"
                >
                  <option v-for="target in board.columns" :key="target.id" :value="target.id">{{ target.name }}</option>
                </select>
              </article>
            </template>
            <div
              v-if="dropTarget?.stageId === column.id && dropTarget?.index === column.cards.length"
              class="h-1 rounded bg-emerald-500/70"
            />
            <p v-if="!column.cards.length && dropTarget?.stageId !== column.id" class="py-6 text-center text-xs text-white/30 light:text-gray-400">
              {{ $t('pipeline.emptyStage') }}
            </p>
            <button
              v-if="column.total > column.cards.length"
              type="button"
              class="w-full py-1.5 text-center text-xs text-white/50 hover:text-white light:text-gray-500 light:hover:text-gray-900 disabled:opacity-50"
              :disabled="loadingMore === column.id"
              @click="loadMore(column)"
            >
              {{ $t('pipeline.loadMore', { n: column.total - column.cards.length }) }}
            </button>
          </div>
        </section>
      </div>
    </div>

    <FollowUpDialog
      v-if="followUpCard"
      v-model:open="followUpOpen"
      :contact-id="followUpCard.id"
      :contact-name="cardName(followUpCard)"
      @created="onFollowUpCreated"
    />

    <LeadFieldsDialog v-model:open="leadFieldsOpen" />
    <NoReplyRuleDialog v-model:open="automationOpen" :pipeline-id="selectedPipelineId" :pipeline-name="board?.pipeline.name" />
    <LeadQueueDialog v-model:open="queueOpen" @claimed="refreshAll" />
    <DuplicatesDialog v-model:open="duplicatesOpen" @merged="refreshAll" />
    <LeadSourcesDialog v-model:open="sourcesOpen" />
    <SalesSettingsDialog v-model:open="salesSettingsOpen" />
    <BulkTemplateDialog v-model:open="bulkTemplateOpen" :contact-ids="[...selectedIds]" @sent="clearSelection" />

    <PipelineEditorDialog
      v-model:open="editorOpen"
      :pipeline-id="editorPipelineId"
      @saved="onEditorSaved"
    />
  </div>
</template>
