<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { MessageSquare, Phone } from 'lucide-vue-next'
import ContactInfoPanel from '@/components/chat/ContactInfoPanel.vue'
import { getAvatarGradient, getInitials } from '@/lib/utils'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'
import { contactsService } from '@/services/api'
import type { Contact } from '@/stores/contacts'

const props = defineProps<{
  open: boolean
  contactId: string
  name: string
  phone: string
}>()

const emit = defineEmits<{
  'update:open': [value: boolean]
  changed: []
}>()

const { t } = useI18n()
const router = useRouter()

// A local copy, not the contacts store: the store's current contact counts as
// "viewing this chat", and incoming messages would be marked read.
const contact = ref<Contact | null>(null)
const sessionData = ref<any>(null)
let edited = false

async function load(id: string) {
  contact.value = null
  sessionData.value = null
  edited = false
  try {
    const [contactResponse, sessionResponse] = await Promise.all([
      contactsService.get(id),
      contactsService.getSessionData(id).catch(() => null)
    ])
    // Ignore a late response for a dialog that was closed or moved on
    if (!props.open || props.contactId !== id) return
    contact.value = unwrapResponse<Contact>(contactResponse)
    sessionData.value = sessionResponse ? unwrapResponse<any>(sessionResponse) : null
  } catch (e) {
    if (!props.open || props.contactId !== id) return
    toast.error(getErrorMessage(e, t('common.loadErrorTitle')))
    emit('update:open', false)
  }
}

watch(
  () => [props.open, props.contactId] as const,
  ([open, id]) => { if (open && id) load(id) },
  { immediate: true }
)

function onChanged(patch: Partial<Contact>) {
  edited = true
  if (contact.value) contact.value = { ...contact.value, ...patch }
}

function onOpenChange(value: boolean) {
  emit('update:open', value)
  if (!value && edited) emit('changed')
}

function openChat() {
  emit('update:open', false)
  router.push(`/chat/${props.contactId}`)
}
</script>

<template>
  <Dialog :open="open" @update:open="onOpenChange">
    <DialogContent class="flex h-[100dvh] max-w-4xl flex-col gap-0 overflow-hidden p-0 sm:h-[85vh]">
      <div class="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-3 border-b p-4 pr-12 sm:pl-6 sm:pr-14">
        <Avatar class="h-14 w-14 shrink-0">
          <AvatarImage :src="contact?.avatar_url ?? ''" />
          <AvatarFallback :class="'text-lg bg-gradient-to-br text-white ' + getAvatarGradient(name || phone)">
            {{ getInitials(name || phone) }}
          </AvatarFallback>
        </Avatar>
        <div class="min-w-0 flex-1">
          <DialogTitle class="truncate text-lg">{{ contact?.name || name || phone }}</DialogTitle>
          <DialogDescription class="mt-0.5 flex items-center gap-1">
            <Phone class="h-3 w-3 shrink-0" />
            <span class="truncate">{{ phone }}</span>
          </DialogDescription>
        </div>
        <Button size="sm" class="w-full sm:w-auto" @click="openChat">
          <MessageSquare class="h-4 w-4 mr-2" />{{ $t('tasks.openChat') }}
        </Button>
      </div>

      <ContactInfoPanel
        v-if="contact"
        embedded
        :contact="contact"
        :session-data="sessionData"
        @changed="onChanged"
        @tags-updated="(tags) => onChanged({ tags })"
      />
      <div v-else class="grid flex-1 gap-8 p-4 sm:p-6 md:grid-cols-2">
        <div v-for="i in 2" :key="i" class="space-y-4">
          <Skeleton class="h-8 w-full" />
          <Skeleton class="h-24 w-full" />
          <Skeleton class="h-16 w-full" />
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
