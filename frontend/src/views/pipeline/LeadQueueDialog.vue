<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { leadsService, type BoardCard } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'

const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ claimed: [] }>()

const { t } = useI18n()
const leads = ref<BoardCard[]>([])
const total = ref(0)
const claiming = ref<string | null>(null)

async function load() {
  try {
    const response = await leadsService.queue({ limit: 50 })
    const data = unwrapResponse<{ leads: BoardCard[]; total: number }>(response)
    leads.value = data.leads || []
    total.value = data.total ?? leads.value.length
  } catch (e) {
    toast.error(getErrorMessage(e, t('queue.failed')))
  }
}

watch(open, value => { if (value) load() })

async function claim(lead: BoardCard) {
  claiming.value = lead.id
  try {
    await leadsService.claim(lead.id)
    toast.success(t('queue.claimed'))
    emit('claimed')
  } catch (e) {
    toast.error(getErrorMessage(e, t('queue.failed')))
  } finally {
    claiming.value = null
    load()
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-w-lg">
      <DialogHeader>
        <DialogTitle>{{ $t('queue.title') }}</DialogTitle>
        <DialogDescription>{{ $t('queue.description', { n: total }) }}</DialogDescription>
      </DialogHeader>
      <p v-if="!leads.length" class="py-8 text-center text-sm text-muted-foreground">{{ $t('queue.empty') }}</p>
      <ul v-else class="max-h-96 divide-y overflow-y-auto rounded-md border text-sm">
        <li v-for="lead in leads" :key="lead.id" class="flex items-center gap-2 px-3 py-2">
          <span class="min-w-0 flex-1">
            <span class="block truncate font-medium">{{ lead.profile_name || lead.phone_number }}</span>
            <span v-if="lead.last_message_preview" class="block truncate text-xs text-muted-foreground">{{ lead.last_message_preview }}</span>
          </span>
          <Button size="sm" variant="outline" class="h-7" :disabled="claiming !== null" @click="claim(lead)">{{ $t('queue.claim') }}</Button>
        </li>
      </ul>
    </DialogContent>
  </Dialog>
</template>
