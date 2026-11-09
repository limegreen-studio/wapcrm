<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FileText, Plus, Trash2 } from 'lucide-vue-next'
import { quotesService, type Quote, type QuoteItem } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'

const props = defineProps<{ contactId: string; canEdit: boolean }>()
const { t } = useI18n()

const quotes = ref<Quote[]>([])
const editorOpen = ref(false)
const isSaving = ref(false)
const busyId = ref<string | null>(null)
const title = ref('')
const items = ref<QuoteItem[]>([])
const taxPercent = ref(0)
const notes = ref('')
const paymentUrl = ref('')

async function load() {
  const contactId = props.contactId
  try {
    const list = unwrapResponse<{ quotes: Quote[] }>(await quotesService.list(contactId)).quotes || []
    if (props.contactId === contactId) quotes.value = list
  } catch {
    quotes.value = []
  }
}

watch(() => props.contactId, load, { immediate: true })

function openEditor() {
  title.value = ''
  items.value = [{ name: '', quantity: 1, price: 0 }]
  taxPercent.value = 0
  notes.value = ''
  paymentUrl.value = ''
  editorOpen.value = true
}

const total = computed(() => {
  const subtotal = items.value.reduce((sum, item) => sum + Number(item.quantity) * Number(item.price), 0)
  return Math.round(subtotal * (100 + Number(taxPercent.value))) / 100
})

async function save() {
  isSaving.value = true
  try {
    await quotesService.create(props.contactId, {
      title: title.value,
      items: items.value.map(item => ({ name: item.name, quantity: Number(item.quantity), price: Number(item.price) })),
      tax_percent: Number(taxPercent.value),
      notes: notes.value,
      payment_url: paymentUrl.value
    })
    editorOpen.value = false
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('quotes.failed')))
  } finally {
    isSaving.value = false
  }
}

async function run(quote: Quote, action: () => Promise<unknown>, success: string) {
  busyId.value = quote.id
  try {
    await action()
    toast.success(success)
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('quotes.failed')))
  } finally {
    busyId.value = null
  }
}

async function copyLink(quote: Quote) {
  try {
    await navigator.clipboard.writeText(quote.link)
    toast.success(t('sources.copied'))
  } catch {
    toast.error(t('quotes.failed'))
  }
}
</script>

<template>
  <div class="pb-4 border-b">
    <div class="flex items-center justify-between py-2">
      <h5 class="text-sm font-medium flex items-center gap-2">
        <FileText class="h-4 w-4 text-muted-foreground" />
        {{ t('quotes.title') }}
      </h5>
      <Button v-if="canEdit" variant="ghost" size="sm" class="h-7 px-2" :aria-label="t('quotes.new')" @click="openEditor">
        <Plus class="h-3.5 w-3.5" />
      </Button>
    </div>
    <p v-if="!quotes.length" class="text-sm text-muted-foreground">{{ t('quotes.empty') }}</p>
    <ul v-else class="space-y-2.5 text-sm">
      <li v-for="quote in quotes" :key="quote.id">
        <p class="truncate font-medium">{{ quote.title }}</p>
        <p class="text-xs text-muted-foreground tabular-nums">{{ quote.total.toLocaleString() }} · {{ t(`quotes.status.${quote.status}`) }}</p>
        <div v-if="canEdit" class="mt-1 flex flex-wrap gap-1.5">
          <Button
            variant="outline" size="sm" class="h-7 text-xs" :disabled="busyId === quote.id"
            @click="run(quote, () => quotesService.send(quote.id), t('quotes.sent'))"
          >{{ t('quotes.send') }}</Button>
          <Button variant="outline" size="sm" class="h-7 text-xs" @click="copyLink(quote)">{{ t('quotes.copyLink') }}</Button>
          <Button
            v-if="quote.status !== 'paid'"
            variant="outline" size="sm" class="h-7 text-xs" :disabled="busyId === quote.id"
            @click="run(quote, () => quotesService.setStatus(quote.id, 'paid'), t('quotes.markedPaid'))"
          >{{ t('quotes.markPaid') }}</Button>
          <Button
            variant="ghost" size="icon" class="h-7 w-7" :disabled="busyId === quote.id" :aria-label="t('quotes.delete')"
            @click="run(quote, () => quotesService.delete(quote.id), t('quotes.deleted'))"
          >
            <Trash2 class="h-3.5 w-3.5" />
          </Button>
        </div>
      </li>
    </ul>

    <Dialog v-model:open="editorOpen">
      <DialogContent class="max-w-lg max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{{ t('quotes.new') }}</DialogTitle>
        </DialogHeader>
        <form class="space-y-4" @submit.prevent="save">
          <div class="space-y-1.5">
            <Label for="quote-title">{{ t('quotes.name') }}</Label>
            <Input id="quote-title" v-model="title" maxlength="255" />
          </div>
          <div class="space-y-2">
            <Label>{{ t('quotes.items') }}</Label>
            <div v-for="(item, i) in items" :key="i" class="grid grid-cols-[1fr_4rem_5.5rem_2.25rem] items-center gap-1.5">
              <Input v-model="item.name" maxlength="255" :placeholder="t('quotes.item')" :aria-label="t('quotes.item')" />
              <Input v-model.number="item.quantity" type="number" min="0" step="any" :aria-label="t('quotes.quantity')" />
              <Input v-model.number="item.price" type="number" min="0" step="any" :aria-label="t('quotes.price')" />
              <Button type="button" variant="ghost" size="icon" class="h-9 w-9" :disabled="items.length === 1" :aria-label="t('quotes.removeItem')" @click="items.splice(i, 1)">
                <Trash2 class="h-4 w-4" />
              </Button>
            </div>
            <Button type="button" variant="outline" size="sm" @click="items.push({ name: '', quantity: 1, price: 0 })">
              <Plus class="h-3.5 w-3.5 mr-1.5" />{{ t('quotes.addItem') }}
            </Button>
          </div>
          <div class="flex items-center gap-2">
            <Label for="quote-tax" class="text-xs">{{ t('quotes.tax') }}</Label>
            <Input id="quote-tax" v-model.number="taxPercent" type="number" min="0" max="100" step="any" class="h-9 w-24" />
            <span class="ml-auto text-sm font-medium tabular-nums">{{ t('quotes.total') }}: {{ total.toLocaleString() }}</span>
          </div>
          <div class="space-y-1.5">
            <Label for="quote-pay">{{ t('quotes.paymentLink') }}</Label>
            <Input id="quote-pay" v-model="paymentUrl" placeholder="https://rzp.io/… or upi://pay?…" />
          </div>
          <div class="space-y-1.5">
            <Label for="quote-notes">{{ t('deal.notes') }}</Label>
            <Textarea id="quote-notes" v-model="notes" :rows="2" />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" @click="editorOpen = false">{{ t('common.cancel') }}</Button>
            <Button type="submit" :disabled="isSaving || !title.trim()">{{ t('common.save') }}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  </div>
</template>
