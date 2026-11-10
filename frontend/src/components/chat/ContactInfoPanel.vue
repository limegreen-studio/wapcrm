<script setup lang="ts">
import { ref, watch, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  Popover,
  PopoverContent,
  PopoverTrigger
} from '@/components/ui/popover'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList
} from '@/components/ui/command'
import { X, ChevronDown, Phone, User, Plus, Check, Tags, Loader2, KanbanSquare, ListTodo, CalendarClock, Pencil } from 'lucide-vue-next'
import { Checkbox } from '@/components/ui/checkbox'
import FollowUpDialog from '@/components/tasks/FollowUpDialog.vue'
import LeadDealSection from '@/components/chat/LeadDealSection.vue'
import LeadQuotes from '@/components/chat/LeadQuotes.vue'
import LeadFieldsSection from '@/components/chat/LeadFieldsSection.vue'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from '@/components/ui/select'
import { TagBadge } from '@/components/ui/tag-badge'
import MetadataSection from '@/components/chat/MetadataSection.vue'
import { getInitials, getAvatarGradient, formatLabel } from '@/lib/utils'
import { getTagColorClass, getStageDotClass } from '@/lib/constants'
import { useTagsStore } from '@/stores/tags'
import { useAuthStore } from '@/stores/auth'
import { usePipelinesStore } from '@/stores/pipelines'
import { useContactsStore } from '@/stores/contacts'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'
import { contactsService, tasksService, type BoardCard, type Tag, type Task } from '@/services/api'
import { wsService } from '@/services/websocket'
import { formatDateTime } from '@/lib/utils'
import { toast } from 'vue-sonner'
import type { Contact } from '@/stores/contacts'

interface PanelFieldConfig {
  key: string
  label: string
  order: number
  display_type?: 'text' | 'badge' | 'tag'
  color?: 'default' | 'success' | 'warning' | 'error' | 'info'
}

interface PanelSection {
  id: string
  label: string
  columns: number
  collapsible: boolean
  default_collapsed: boolean
  order: number
  fields: PanelFieldConfig[]
}

interface PanelConfig {
  sections: PanelSection[]
}

interface SessionData {
  session_id?: string
  flow_id?: string
  flow_name?: string
  session_data: Record<string, any>
  panel_config: PanelConfig
}

const props = defineProps<{
  contact: Contact
  sessionData?: SessionData | null
  // Rendered inside a dialog: the host supplies the header and identity block
  embedded?: boolean
}>()

const emit = defineEmits<{
  close: []
  tagsUpdated: [tags: string[]]
  changed: [patch: Partial<Contact>]
}>()

const { t } = useI18n()
const tagsStore = useTagsStore()
const authStore = useAuthStore()
const pipelinesStore = usePipelinesStore()
const contactsStore = useContactsStore()
const collapsedSections = ref<Record<string, boolean>>({})
const tagSelectorOpen = ref(false)
const isUpdatingTags = ref(false)

// Resizable panel state
const MIN_WIDTH = 280
const MAX_WIDTH = 480
const panelWidth = ref(MAX_WIDTH) // Default to max width
const isResizing = ref(false)

// Check if user can edit tags
const canEditTags = computed(() => authStore.hasPermission('contacts', 'write'))

// Fetch tags on mount
onMounted(async () => {
  if (tagsStore.tags.length === 0) {
    try { await tagsStore.fetchTags() } catch { /* tags won't be available */ }
  }
})

// Pipeline stage
const NO_STAGE = '__none__'
const canMoveStage = computed(() => authStore.hasPermission('chat', 'write'))
const isUpdatingStage = ref(false)
const stageValue = computed(() => props.contact.stage_id ?? NO_STAGE)
const currentStage = computed(() =>
  props.contact.stage_id ? pipelinesStore.stageIndex.get(props.contact.stage_id) : undefined
)

onMounted(async () => {
  try { await pipelinesStore.fetchPipelines() } catch { /* stage picker won't be available */ }
})

// Follow-up tasks for this contact
const openTasks = ref<Task[]>([])
const followUpOpen = ref(false)
const editingTask = ref<Task | null>(null)
const taskEditOpen = ref(false)

// Won and lost leads can start a new deal
const isClosedStage = computed(() => {
  const id = props.contact.stage_id
  if (!id) return false
  const stage = pipelinesStore.pipelines.flatMap(p => p.stages).find(s => s.id === id)
  return !!stage && stage.kind !== 'open'
})

// Keep the open chat's contact in step, and tell a host that owns its own copy
function patchContact(id: string, patch: Partial<Contact>) {
  const current = contactsStore.currentContact
  if (current && current.id === id) Object.assign(current, patch)
  if (id === props.contact.id) emit('changed', patch)
}

function onNewDeal(card: BoardCard) {
  patchContact(card.id, {
    stage_id: card.stage_id,
    stage_entered_at: card.stage_entered_at,
    deal_value: 0,
    close_reason: ''
  })
}

function onDealSaved(dealValue: number, closeReason: string) {
  patchContact(props.contact.id, { deal_value: dealValue, close_reason: closeReason })
}

function onDncChanged(value: boolean) {
  patchContact(props.contact.id, { do_not_contact: value })
}

function openTaskEdit(task: Task) {
  editingTask.value = task
  taskEditOpen.value = true
}

async function loadTasks() {
  const contactId = props.contact.id
  try {
    const response = await tasksService.list({ contact_id: contactId, status: 'open', limit: 20 })
    // Ignore a late response for a previously selected contact
    if (props.contact.id !== contactId) return
    openTasks.value = unwrapResponse<{ tasks: Task[] }>(response).tasks || []
  } catch {
    openTasks.value = []
  }
}

watch(() => props.contact.id, loadTasks, { immediate: true })

function onTasksEdited() {
  loadTasks()
  emit('changed', {})
}

async function completeTask(task: Task) {
  try {
    await tasksService.update(task.id, { done: true })
    openTasks.value = openTasks.value.filter(x => x.id !== task.id)
    emit('changed', {})
    toast.success(t('tasks.markedDone'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('tasks.failedSave')))
  }
}

const unsubscribeTasks = wsService.onTaskChanged((payload: any) => {
  if (payload.contact_id === props.contact.id) loadTasks()
})
onUnmounted(unsubscribeTasks)

async function updateStage(value: unknown) {
  const stageId = value === NO_STAGE ? null : String(value)
  if (stageId === (props.contact.stage_id ?? null) || isUpdatingStage.value) return
  isUpdatingStage.value = true
  try {
    const card = await pipelinesStore.moveContact(props.contact.id, stageId)
    patchContact(card.id, {
      pipeline_id: card.pipeline_id,
      stage_id: card.stage_id,
      stage_entered_at: card.stage_entered_at
    })
    toast.success(t('pipeline.stageUpdated'))
  } catch (e) {
    toast.error(getErrorMessage(e, t('pipeline.failedMove')))
  } finally {
    isUpdatingStage.value = false
  }
}

function startResize(e: MouseEvent) {
  isResizing.value = true
  const startX = e.clientX
  const startWidth = panelWidth.value

  function onMouseMove(e: MouseEvent) {
    // Dragging left increases width, dragging right decreases
    const delta = startX - e.clientX
    const newWidth = Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, startWidth + delta))
    panelWidth.value = newWidth
  }

  function onMouseUp() {
    isResizing.value = false
    document.removeEventListener('mousemove', onMouseMove)
    document.removeEventListener('mouseup', onMouseUp)
  }

  document.addEventListener('mousemove', onMouseMove)
  document.addEventListener('mouseup', onMouseUp)
}

// Initialize collapsed state when session data changes
watch(() => props.sessionData, (newData) => {
  if (newData?.panel_config?.sections) {
    for (const section of newData.panel_config.sections) {
      if (collapsedSections.value[section.id] === undefined) {
        collapsedSections.value[section.id] = section.default_collapsed
      }
    }
  }
}, { immediate: true })

function toggleSection(sectionId: string) {
  collapsedSections.value[sectionId] = !collapsedSections.value[sectionId]
}

function isSectionCollapsed(sectionId: string): boolean {
  return collapsedSections.value[sectionId] ?? false
}

function getFieldValue(key: string): string {
  if (!props.sessionData?.session_data) return '-'
  const value = props.sessionData.session_data[key]
  if (value === undefined || value === null || value === '') return '-'
  return String(value)
}

function getColorClass(color?: string): string {
  switch (color) {
    case 'success':
      return 'bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400'
    case 'warning':
      return 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400'
    case 'error':
      return 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-400'
    case 'info':
      return 'bg-blue-100 text-blue-800 dark:bg-blue-900/30 dark:text-blue-400'
    default:
      return 'bg-muted text-muted-foreground'
  }
}

// Sort sections by order
const sortedSections = computed(() => {
  if (!props.sessionData?.panel_config?.sections) return []
  return [...props.sessionData.panel_config.sections].sort((a, b) => a.order - b.order)
})

// Get tags from contact
const contactTags = computed(() => {
  if (!props.contact.tags || !Array.isArray(props.contact.tags)) return []
  return props.contact.tags as string[]
})

// Contact metadata (metadata.lead is shown by LeadFieldsSection instead)
const otherMetadata = computed(() => {
  const md = props.contact.metadata
  if (!md || typeof md !== 'object') return []
  return Object.entries(md).filter(([k]) => k !== 'lead')
})

const hasMetadata = computed(() => otherMetadata.value.length > 0)

const metadataPrimitives = computed(() =>
  otherMetadata.value.filter(([, v]) => v === null || typeof v !== 'object')
)

const metadataSections = computed(() =>
  otherMetadata.value.filter(([, v]) => v !== null && typeof v === 'object')
)

function onLeadUpdated(metadata: Record<string, any>) {
  patchContact(props.contact.id, { metadata })
}

// Get tag details for display
function getTagDetails(tagName: string): Tag | undefined {
  return tagsStore.getTagByName(tagName)
}

// Check if a tag is selected
function isTagSelected(tagName: string): boolean {
  return contactTags.value.includes(tagName)
}

// Toggle tag on contact
async function toggleTag(tagName: string) {
  if (isUpdatingTags.value) return

  const currentTags = [...contactTags.value]
  let newTags: string[]

  if (currentTags.includes(tagName)) {
    newTags = currentTags.filter(t => t !== tagName)
  } else {
    newTags = [...currentTags, tagName]
  }

  await updateContactTags(newTags)
}

// Remove tag from contact
async function removeTag(tagName: string) {
  if (isUpdatingTags.value) return

  const newTags = contactTags.value.filter(t => t !== tagName)
  await updateContactTags(newTags)
}

// Update contact tags via API
async function updateContactTags(tags: string[]) {
  isUpdatingTags.value = true
  try {
    await contactsService.updateTags(props.contact.id, tags)
    emit('tagsUpdated', tags)
    toast.success('Tags updated')
  } catch (e: any) {
    toast.error(e.response?.data?.message || 'Failed to update tags')
  } finally {
    isUpdatingTags.value = false
  }
}

</script>

<template>
  <div
    class="flex flex-col"
    :class="embedded ? 'flex-1 min-h-0' : 'bg-card fixed inset-x-0 bottom-0 top-12 z-40 w-full md:relative md:inset-auto md:top-auto md:z-auto md:h-full md:w-[var(--panel-w)]'"
    :style="{ '--panel-w': `${panelWidth}px` }"
  >
    <!-- Resize Handle -->
    <div
      v-if="!embedded"
      class="absolute left-0 top-0 bottom-0 w-1 cursor-col-resize hover:bg-primary/20 active:bg-primary/30 z-10 border-l"
      :class="{ 'bg-primary/30': isResizing }"
      @mousedown="startResize"
    />

    <!-- Header -->
    <div v-if="!embedded" class="h-12 px-3 border-b flex items-center justify-between">
      <h3 class="font-medium text-sm">Contact Info</h3>
      <Button variant="ghost" size="icon" class="h-8 w-8" @click="emit('close')">
        <X class="h-4 w-4" />
      </Button>
    </div>

    <ScrollArea class="flex-1">
      <div :class="embedded ? 'grid gap-x-8 gap-y-4 p-4 sm:p-6 md:grid-cols-2 md:items-start' : 'p-4 space-y-4'">
        <!-- Contact Header -->
        <div v-if="!embedded" class="flex flex-col items-center text-center pb-4 border-b">
          <Avatar class="h-16 w-16 mb-3">
            <AvatarImage :src="contact.avatar_url" />
            <AvatarFallback :class="'text-lg bg-gradient-to-br text-white ' + getAvatarGradient(contact.name || contact.phone_number)">
              {{ getInitials(contact.name || contact.phone_number) }}
            </AvatarFallback>
          </Avatar>
          <h4 class="font-medium">
            {{ contact.name || contact.phone_number }}
          </h4>
          <div class="flex items-center gap-1 text-sm text-muted-foreground mt-1">
            <Phone class="h-3 w-3" />
            <span>{{ contact.phone_number }}</span>
          </div>
        </div>

        <div class="min-w-0 space-y-4">
          <!-- Pipeline Stage -->
          <div v-if="pipelinesStore.pipelines.length" class="pb-4 border-b">
            <h5 class="text-sm font-medium flex items-center gap-2 py-2">
              <KanbanSquare class="h-4 w-4 text-muted-foreground" />
              {{ t('pipeline.stage') }}
              <Loader2 v-if="isUpdatingStage" class="h-3.5 w-3.5 animate-spin text-muted-foreground" />
            </h5>
            <Select
              v-if="canMoveStage"
              :model-value="stageValue"
              :disabled="isUpdatingStage"
              @update:model-value="updateStage"
            >
              <SelectTrigger class="h-8 text-sm" :aria-label="t('pipeline.stage')">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem :value="NO_STAGE">{{ t('pipeline.notInPipeline') }}</SelectItem>
                <SelectGroup v-for="pipeline in pipelinesStore.pipelines" :key="pipeline.id">
                  <SelectLabel v-if="pipelinesStore.pipelines.length > 1">{{ pipeline.name }}</SelectLabel>
                  <SelectItem v-for="stage in pipeline.stages" :key="stage.id" :value="stage.id">
                    <div class="flex items-center gap-2">
                      <span :class="['w-2.5 h-2.5 rounded-full', getStageDotClass(stage.color)]" />
                      {{ stage.name }}
                    </div>
                  </SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <p v-else class="text-sm text-muted-foreground">
              {{ currentStage ? currentStage.stage.name : t('pipeline.notInPipeline') }}
            </p>
          </div>

          <!-- Lead fields -->
          <LeadFieldsSection
            :contact-id="contact.id"
            :metadata="contact.metadata"
            :can-edit="canMoveStage"
            @updated="onLeadUpdated"
          />

          <!-- Follow-ups -->
          <div class="pb-4 border-b">
            <div class="flex items-center justify-between py-2">
              <h5 class="text-sm font-medium flex items-center gap-2">
                <ListTodo class="h-4 w-4 text-muted-foreground" />
                {{ t('tasks.followUps') }}
              </h5>
              <Button
                v-if="canMoveStage"
                variant="ghost"
                size="sm"
                class="h-7 px-2"
                :aria-label="t('tasks.newFollowUp')"
                @click="followUpOpen = true"
              >
                <Plus class="h-3.5 w-3.5" />
              </Button>
            </div>
            <p v-if="!openTasks.length" class="text-sm text-muted-foreground">{{ t('tasks.noOpenFollowUps') }}</p>
            <ul v-else class="space-y-2">
              <li v-for="task in openTasks" :key="task.id" class="flex items-start gap-2 text-sm">
                <Checkbox
                  class="mt-0.5"
                  :disabled="!canMoveStage"
                  :aria-label="t('tasks.markDone')"
                  @update:checked="(v) => v === true && completeTask(task)"
                />
                <div class="min-w-0 flex-1">
                  <p class="truncate">{{ task.title }}</p>
                  <p
                    v-if="task.due_at"
                    class="flex items-center gap-1 text-xs"
                    :class="task.overdue ? 'text-red-500' : 'text-muted-foreground'"
                  >
                    <CalendarClock class="h-3 w-3" />{{ formatDateTime(task.due_at) }}
                  </p>
                </div>
                <Button
                  v-if="canMoveStage"
                  variant="ghost"
                  size="icon"
                  class="h-6 w-6 shrink-0"
                  :aria-label="t('tasks.editFollowUp')"
                  @click="openTaskEdit(task)"
                >
                  <Pencil class="h-3 w-3" />
                </Button>
              </li>
            </ul>
            <FollowUpDialog
              v-model:open="followUpOpen"
              :contact-id="contact.id"
              :contact-name="contact.name || contact.phone_number"
              @created="onTasksEdited"
            />
            <FollowUpDialog
              v-if="editingTask"
              v-model:open="taskEditOpen"
              :contact-id="contact.id"
              :contact-name="contact.name || contact.phone_number"
              :task="editingTask"
              @saved="onTasksEdited"
            />
          </div>
        </div>

        <div class="min-w-0 space-y-4">
          <LeadDealSection
            :contact-id="contact.id"
            :phone="contact.phone_number"
            :deal-value="contact.deal_value"
            :close-reason="contact.close_reason"
            :do-not-contact="contact.do_not_contact"
            :is-closed="isClosedStage"
            :can-edit="canMoveStage"
            @logged="onTasksEdited"
            @deal-saved="onDealSaved"
            @new-deal="onNewDeal"
            @dnc-changed="onDncChanged"
          />

          <LeadQuotes :contact-id="contact.id" :can-edit="canMoveStage" />

          <!-- Tags Section (always shown) -->
          <div class="pb-4">
            <div class="flex items-center justify-between py-2">
              <h5 class="text-sm font-medium flex items-center gap-2">
                <Tags class="h-4 w-4 text-muted-foreground" />
                Tags
              </h5>
              <Popover v-if="canEditTags" v-model:open="tagSelectorOpen">
                <PopoverTrigger as-child>
                  <Button variant="ghost" size="sm" class="h-7 px-2">
                    <Plus class="h-3.5 w-3.5" />
                  </Button>
                </PopoverTrigger>
                <PopoverContent class="w-[200px] p-0" align="end">
                  <Command>
                    <CommandInput placeholder="Search tags..." />
                    <CommandList>
                      <CommandEmpty>
                        <div class="py-4 text-center text-sm text-muted-foreground">
                          No tags found
                        </div>
                      </CommandEmpty>
                      <CommandGroup>
                        <CommandItem
                          v-for="tag in tagsStore.tags"
                          :key="tag.name"
                          :value="tag.name"
                          class="flex items-center gap-2"
                          @select="toggleTag(tag.name)"
                        >
                          <div class="flex items-center gap-2 flex-1">
                            <span :class="['w-2 h-2 rounded-full', getTagColorClass(tag.color).split(' ')[0]]"></span>
                            <span>{{ tag.name }}</span>
                          </div>
                          <Check v-if="isTagSelected(tag.name)" class="h-4 w-4 text-primary" />
                        </CommandItem>
                      </CommandGroup>
                    </CommandList>
                  </Command>
                </PopoverContent>
              </Popover>
            </div>

            <div class="flex flex-wrap gap-2 mt-2">
              <template v-if="contactTags.length > 0">
                <TagBadge
                  v-for="tagName in contactTags"
                  :key="tagName"
                  :color="getTagDetails(tagName)?.color"
                >
                  {{ tagName }}
                  <button
                    v-if="canEditTags"
                    type="button"
                    class="ml-1 rounded-full hover:bg-black/10 dark:hover:bg-white/10 p-0.5 transition-colors"
                    :disabled="isUpdatingTags"
                    @click.stop="removeTag(tagName)"
                  >
                    <X class="h-3 w-3" />
                  </button>
                </TagBadge>
              </template>
              <span v-else class="text-sm text-muted-foreground">No tags</span>
              <Loader2 v-if="isUpdatingTags" class="h-4 w-4 animate-spin text-muted-foreground" />
            </div>
          </div>

          <!-- Contact Metadata -->
          <div v-if="hasMetadata" class="space-y-3">
            <!-- General section: top-level primitives -->
            <MetadataSection
              v-if="metadataPrimitives.length > 0"
              label="General"
              :data="Object.fromEntries(metadataPrimitives)"
            />
            <!-- Object / array sections -->
            <MetadataSection
              v-for="[key, val] in metadataSections"
              :key="key"
              :label="formatLabel(key)"
              :data="val"
            />
          </div>

          <!-- No Session Data or no panel config -->
          <div v-if="!embedded && (!props.sessionData || sortedSections.length === 0)" class="text-center py-6 text-muted-foreground border-t">
            <User class="h-8 w-8 mx-auto mb-2 opacity-50" />
            <p class="text-sm">No data configured</p>
            <p class="text-xs mt-1">Configure panel display in the chatbot flow settings.</p>
          </div>

          <!-- Session Data with panel config -->
          <template v-else-if="props.sessionData && sortedSections.length > 0">
            <!-- Flow Name Badge -->
            <div v-if="props.sessionData?.flow_name" class="flex items-center gap-2">
              <Badge variant="outline" class="text-xs">
                {{ props.sessionData?.flow_name }}
              </Badge>
            </div>

            <!-- Dynamic Sections -->
            <div v-for="section in sortedSections" :key="section.id" class="space-y-2 border-t pt-4">
              <Collapsible
                v-if="section.collapsible"
                :open="!isSectionCollapsed(section.id)"
                @update:open="toggleSection(section.id)"
              >
                <CollapsibleTrigger class="flex items-center justify-between w-full py-2 text-sm font-medium hover:text-primary transition-colors">
                  <span>{{ section.label }}</span>
                  <ChevronDown
                    :class="[
                      'h-4 w-4 transition-transform',
                      isSectionCollapsed(section.id) ? '-rotate-90' : ''
                    ]"
                  />
                </CollapsibleTrigger>
                <CollapsibleContent>
                  <div
                    :class="[
                      'grid gap-2 pt-2',
                      section.columns === 2 ? 'grid-cols-2' : 'grid-cols-1'
                    ]"
                  >
                    <div
                      v-for="field in section.fields.sort((a, b) => a.order - b.order)"
                      :key="field.key"
                      class="bg-muted/50 rounded-md px-3 py-2"
                    >
                      <p class="text-[10px] uppercase tracking-wide text-muted-foreground font-medium">{{ field.label }}</p>
                      <!-- Badge display -->
                      <span
                        v-if="field.display_type === 'badge'"
                        :class="['inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-semibold mt-1', getColorClass(field.color)]"
                      >
                        {{ getFieldValue(field.key) }}
                      </span>
                      <!-- Tag display -->
                      <span
                        v-else-if="field.display_type === 'tag'"
                        :class="['inline-flex items-center rounded-md px-2 py-1 text-xs font-medium mt-1', getColorClass(field.color)]"
                      >
                        {{ getFieldValue(field.key) }}
                      </span>
                      <!-- Default text display -->
                      <p v-else class="text-sm font-semibold break-words mt-0.5">{{ getFieldValue(field.key) }}</p>
                    </div>
                  </div>
                </CollapsibleContent>
              </Collapsible>

              <!-- Non-collapsible section -->
              <div v-else>
                <h5 class="py-2 text-sm font-medium">{{ section.label }}</h5>
                <div
                  :class="[
                    'grid gap-2',
                    section.columns === 2 ? 'grid-cols-2' : 'grid-cols-1'
                  ]"
                >
                  <div
                    v-for="field in section.fields.sort((a, b) => a.order - b.order)"
                    :key="field.key"
                    class="bg-muted/50 rounded-md px-3 py-2"
                  >
                    <p class="text-[10px] uppercase tracking-wide text-muted-foreground font-medium">{{ field.label }}</p>
                    <!-- Badge display -->
                    <span
                      v-if="field.display_type === 'badge'"
                      :class="['inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-semibold mt-1', getColorClass(field.color)]"
                    >
                      {{ getFieldValue(field.key) }}
                    </span>
                    <!-- Tag display -->
                    <span
                      v-else-if="field.display_type === 'tag'"
                      :class="['inline-flex items-center rounded-md px-2 py-1 text-xs font-medium mt-1', getColorClass(field.color)]"
                    >
                      {{ getFieldValue(field.key) }}
                    </span>
                    <!-- Default text display -->
                    <p v-else class="text-sm font-semibold break-words mt-0.5">{{ getFieldValue(field.key) }}</p>
                  </div>
                </div>
              </div>
            </div>
          </template>
        </div>
      </div>
    </ScrollArea>
  </div>
</template>
