<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { IndianRupee, Phone, PhoneCall, History, RotateCcw } from 'lucide-vue-next'
import { leadsService, type BoardCard, type CallOutcome, type TimelineEntry } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'
import { formatDateTime } from '@/lib/utils'
import { telHref } from '@/lib/pipelineBoard'

const props = defineProps<{
  contactId: string
  phone?: string
  dealValue?: number
  closeReason?: string
  doNotContact?: boolean
  isClosed?: boolean
  canEdit: boolean
}>()

const emit = defineEmits<{ logged: []; newDeal: [card: BoardCard]; dncChanged: [value: boolean]; dealSaved: [dealValue: number, closeReason: string] }>()

const { t } = useI18n()

const value = ref('')
const reason = ref('')
const savedValue = ref('')
const savedReason = ref('')

watch(
  () => [props.contactId, props.dealValue, props.closeReason] as const,
  () => {
    savedValue.value = value.value = props.dealValue ? String(props.dealValue) : ''
    savedReason.value = reason.value = props.closeReason ?? ''
  },
  { immediate: true }
)

async function saveDeal() {
  if (value.value === savedValue.value && reason.value === savedReason.value) return
  const parsed = value.value === '' ? 0 : Number(value.value)
  if (Number.isNaN(parsed) || parsed < 0) {
    value.value = savedValue.value
    toast.error(t('deal.failedSave'))
    return
  }
  try {
    await leadsService.updateDeal(props.contactId, { deal_value: parsed, close_reason: reason.value })
    savedValue.value = value.value
    savedReason.value = reason.value
    emit('dealSaved', parsed, reason.value)
  } catch (e) {
    value.value = savedValue.value
    reason.value = savedReason.value
    toast.error(getErrorMessage(e, t('deal.failedSave')))
  }
}

const OUTCOMES: CallOutcome[] = ['connected', 'no_answer', 'busy', 'callback', 'wrong_number']
const callOpen = ref(false)
const outcome = ref<CallOutcome>('connected')
const callNotes = ref('')
const followUpAt = ref('')
const isLogging = ref(false)

function openCall() {
  outcome.value = 'connected'
  callNotes.value = ''
  followUpAt.value = ''
  callOpen.value = true
}

async function logCall() {
  isLogging.value = true
  try {
    await leadsService.logCall(props.contactId, {
      outcome: outcome.value,
      notes: callNotes.value || undefined,
      follow_up_at: followUpAt.value ? new Date(followUpAt.value).toISOString() : undefined
    })
    toast.success(t('deal.logged'))
    callOpen.value = false
    emit('logged')
  } catch (e) {
    toast.error(getErrorMessage(e, t('deal.failedLog')))
  } finally {
    isLogging.value = false
  }
}

const historyOpen = ref(false)
const timeline = ref<TimelineEntry[]>([])

async function loadTimeline() {
  try {
    timeline.value = unwrapResponse<{ entries: TimelineEntry[] }>(await leadsService.timeline(props.contactId)).entries || []
  } catch {
    timeline.value = []
  }
}

async function toggleHistory() {
  historyOpen.value = !historyOpen.value
  if (historyOpen.value) await loadTimeline()
}

watch(() => props.contactId, () => {
  historyOpen.value = false
  timeline.value = []
})

const isStartingDeal = ref(false)

async function startNewDeal() {
  isStartingDeal.value = true
  try {
    const card = unwrapResponse<BoardCard>(await leadsService.newDeal(props.contactId))
    savedValue.value = value.value = ''
    savedReason.value = reason.value = ''
    toast.success(t('deal.newDealStarted'))
    emit('newDeal', card)
    if (historyOpen.value) loadTimeline()
  } catch (e) {
    toast.error(getErrorMessage(e, t('deal.failedNewDeal')))
  } finally {
    isStartingDeal.value = false
  }
}

const dnc = ref(false)
watch(() => [props.contactId, props.doNotContact] as const, () => { dnc.value = !!props.doNotContact }, { immediate: true })

async function setDnc(next: boolean) {
  const previous = dnc.value
  dnc.value = next
  try {
    await leadsService.setDoNotContact(props.contactId, next)
    emit('dncChanged', next)
  } catch (e) {
    dnc.value = previous
    toast.error(getErrorMessage(e, t('deal.failedSave')))
  }
}
</script>

<template>
  <div class="pb-4 border-b">
    <h5 class="text-sm font-medium flex items-center gap-2 py-2">
      <IndianRupee class="h-4 w-4 text-muted-foreground" />
      {{ t('deal.title') }}
    </h5>
    <dl class="grid grid-cols-[6rem_1fr] items-center gap-x-2 gap-y-1.5 text-sm">
      <dt class="text-muted-foreground"><label for="deal-value">{{ t('deal.value') }}</label></dt>
      <dd class="min-w-0">
        <Input
          v-if="canEdit"
          id="deal-value"
          v-model="value"
          class="h-7 text-sm"
          type="number"
          min="0"
          step="0.01"
          @blur="saveDeal"
          @keydown.enter.prevent="($event.target as HTMLInputElement).blur()"
        />
        <span v-else>{{ savedValue || '—' }}</span>
      </dd>
      <dt class="text-muted-foreground"><label for="deal-reason">{{ t('deal.reason') }}</label></dt>
      <dd class="min-w-0">
        <Input
          v-if="canEdit"
          id="deal-reason"
          v-model="reason"
          class="h-7 text-sm"
          maxlength="255"
          :placeholder="t('deal.reasonPlaceholder')"
          @blur="saveDeal"
          @keydown.enter.prevent="($event.target as HTMLInputElement).blur()"
        />
        <span v-else class="block truncate">{{ savedReason || '—' }}</span>
      </dd>
    </dl>

    <div class="mt-2 flex flex-wrap gap-2">
      <Button v-if="phone && telHref(phone)" as="a" :href="telHref(phone)!" variant="outline" size="sm" class="h-7 text-xs">
        <Phone class="h-3 w-3 mr-1.5" />{{ t('deal.call') }}
      </Button>
      <Button v-if="canEdit" variant="outline" size="sm" class="h-7 text-xs" @click="openCall">
        <PhoneCall class="h-3 w-3 mr-1.5" />{{ t('deal.logCall') }}
      </Button>
      <Button v-if="canEdit && isClosed" variant="outline" size="sm" class="h-7 text-xs" :disabled="isStartingDeal" @click="startNewDeal">
        <RotateCcw class="h-3 w-3 mr-1.5" />{{ t('deal.newDeal') }}
      </Button>
      <Button variant="ghost" size="sm" class="h-7 text-xs" :aria-expanded="historyOpen" @click="toggleHistory">
        <History class="h-3 w-3 mr-1.5" />{{ t('deal.history') }}
      </Button>
    </div>

    <label v-if="canEdit" class="mt-2 flex items-center gap-2 text-xs">
      <Switch :checked="dnc" @update:checked="setDnc" />
      {{ t('deal.doNotContact') }}
    </label>
    <p v-else-if="dnc" class="mt-2 text-xs text-amber-500">{{ t('deal.doNotContact') }}</p>

    <template v-if="historyOpen">
      <p v-if="!timeline.length" class="mt-2 text-xs text-muted-foreground">{{ t('deal.noHistory') }}</p>
      <ol v-else class="mt-2 max-h-72 space-y-2 overflow-y-auto text-xs">
        <li v-for="(entry, i) in timeline" :key="i">
          <span class="whitespace-pre-line">{{ entry.text }}</span>
          <span class="block text-muted-foreground">{{ formatDateTime(entry.at) }}<template v-if="entry.by"> · {{ entry.by }}</template></span>
        </li>
      </ol>
    </template>

    <Dialog v-model:open="callOpen">
      <DialogContent class="max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('deal.logCallTitle') }}</DialogTitle>
        </DialogHeader>
        <form class="space-y-4" @submit.prevent="logCall">
          <div class="space-y-1.5">
            <Label for="call-outcome">{{ t('deal.outcome') }}</Label>
            <Select v-model="outcome">
              <SelectTrigger id="call-outcome"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="o in OUTCOMES" :key="o" :value="o">{{ t(`deal.outcomes.${o}`) }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-1.5">
            <Label for="call-notes">{{ t('deal.notes') }}</Label>
            <Textarea id="call-notes" v-model="callNotes" :rows="3" />
          </div>
          <div class="space-y-1.5">
            <Label for="call-followup">{{ t('deal.followUp') }}</Label>
            <Input id="call-followup" v-model="followUpAt" type="datetime-local" />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" @click="callOpen = false">{{ t('common.cancel') }}</Button>
            <Button type="submit" :disabled="isLogging">{{ t('common.save') }}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  </div>
</template>
