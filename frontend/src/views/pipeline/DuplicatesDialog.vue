<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { leadsService, type BoardCard } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'

interface Group { key: string; contacts: BoardCard[] }

const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ merged: [] }>()

const { t } = useI18n()
const groups = ref<Group[]>([])
const busy = ref(false)
// Merging cannot be undone, so the first click only arms this pair
const armed = ref<string | null>(null)

async function load() {
  armed.value = null
  try {
    const response = await leadsService.duplicates()
    groups.value = unwrapResponse<{ groups: Group[] }>(response).groups || []
  } catch (e) {
    toast.error(getErrorMessage(e, t('duplicates.failed')))
  }
}

watch(open, value => { if (value) load() })

function others(group: Group, keep: BoardCard) {
  return group.contacts.filter(c => c.id !== keep.id)
}

async function merge(keep: BoardCard, drop: BoardCard) {
  const key = `${keep.id}:${drop.id}`
  if (armed.value !== key) {
    armed.value = key
    return
  }
  busy.value = true
  try {
    await leadsService.merge(keep.id, drop.id)
    toast.success(t('duplicates.merged'))
    emit('merged')
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('duplicates.failed')))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-w-2xl">
      <DialogHeader>
        <DialogTitle>{{ $t('duplicates.title') }}</DialogTitle>
        <DialogDescription>{{ $t('duplicates.description') }}</DialogDescription>
      </DialogHeader>
      <p v-if="!groups.length" class="py-8 text-center text-sm text-muted-foreground">{{ $t('duplicates.empty') }}</p>
      <div v-else class="max-h-[28rem] space-y-3 overflow-y-auto">
        <section v-for="group in groups" :key="group.key" class="rounded-md border p-3 text-sm">
          <div v-for="contact in group.contacts" :key="contact.id" class="py-1.5">
            <p class="font-medium">{{ contact.profile_name || contact.phone_number }}
              <span class="ml-1 text-xs font-normal text-muted-foreground">{{ contact.phone_number }}</span>
            </p>
            <div class="mt-1 flex flex-wrap gap-1.5">
              <Button
                v-for="drop in others(group, contact)"
                :key="drop.id"
                size="sm"
                :variant="armed === `${contact.id}:${drop.id}` ? 'destructive' : 'outline'"
                class="h-7 text-xs"
                :disabled="busy"
                @click="merge(contact, drop)"
              >{{ armed === `${contact.id}:${drop.id}` ? $t('duplicates.confirm') : $t('duplicates.keep', { phone: drop.phone_number }) }}</Button>
            </div>
          </div>
        </section>
      </div>
    </DialogContent>
  </Dialog>
</template>
