<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useAuthStore } from '@/stores/auth'
import { useUsersStore } from '@/stores/users'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { tasksService, type Task } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'
import { DUE_PRESETS, daysAtTen, toLocalInput } from '@/lib/taskDue'

const props = defineProps<{
  contactId: string
  contactName?: string
  task?: Task | null // set to edit an existing follow-up
}>()

const open = defineModel<boolean>('open', { default: false })

const emit = defineEmits<{
  created: [task: Task]
  saved: [task: Task]
}>()

const { t } = useI18n()
const authStore = useAuthStore()
const usersStore = useUsersStore()

// Picking another assignee needs the user list, which needs users:read
// (same rule as the chat assign menu). Others always assign to themselves.
const ME = '__me__'
const canPickAssignee = computed(() => authStore.hasPermission('users', 'read'))
const assignee = ref<string>(ME)
const assignableUsers = computed(() =>
  usersStore.users.filter(u => u.is_active && u.id !== authStore.user?.id)
)
const isEdit = computed(() => !!props.task)

const title = ref('')
const notes = ref('')
const dueLocal = ref('') // value of <input type="datetime-local">
const isSaving = ref(false)

const presets = DUE_PRESETS

watch(open, value => {
  if (!value) return
  const task = props.task
  title.value = task ? task.title : t('tasks.defaultTitle')
  notes.value = task?.notes ?? ''
  dueLocal.value = task
    ? (task.due_at ? toLocalInput(new Date(task.due_at)) : '')
    : toLocalInput(daysAtTen(1))
  assignee.value = !task?.assignee_id || task.assignee_id === authStore.user?.id ? ME : task.assignee_id
  if (canPickAssignee.value && usersStore.users.length === 0) {
    usersStore.fetchUsers().catch(() => { /* picker falls back to "me" */ })
  }
})

async function save() {
  if (!title.value.trim()) {
    toast.error(t('tasks.titleRequired'))
    return
  }
  isSaving.value = true
  try {
    if (props.task) {
      const nextAssignee = assignee.value === ME ? authStore.user?.id : assignee.value
      const response = await tasksService.update(props.task.id, {
        title: title.value.trim(),
        notes: notes.value,
        ...(dueLocal.value ? { due_at: new Date(dueLocal.value).toISOString() } : { clear_due_at: true }),
        ...(canPickAssignee.value && nextAssignee ? { assignee_id: nextAssignee } : {})
      })
      toast.success(t('tasks.saved'))
      open.value = false
      emit('saved', unwrapResponse<Task>(response))
      return
    }
    const response = await tasksService.create({
      contact_id: props.contactId,
      title: title.value.trim(),
      notes: notes.value || undefined,
      due_at: dueLocal.value ? new Date(dueLocal.value).toISOString() : undefined,
      assignee_id: assignee.value === ME ? undefined : assignee.value
    })
    const task = unwrapResponse<Task>(response)
    toast.success(t('tasks.created'))
    open.value = false
    emit('created', task)
  } catch (e) {
    toast.error(getErrorMessage(e, t('tasks.failedSave')))
  } finally {
    isSaving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-w-md">
      <DialogHeader>
        <DialogTitle>{{ isEdit ? $t('tasks.editFollowUp') : $t('tasks.newFollowUp') }}</DialogTitle>
        <DialogDescription v-if="contactName">{{ $t('tasks.forContact', { name: contactName }) }}</DialogDescription>
      </DialogHeader>

      <form class="space-y-4" @submit.prevent="save">
        <div class="space-y-1.5">
          <Label for="followup-title">{{ $t('tasks.title') }}</Label>
          <Input id="followup-title" v-model="title" maxlength="255" autofocus />
        </div>

        <div class="space-y-1.5">
          <Label for="followup-due">{{ $t('tasks.due') }}</Label>
          <Input id="followup-due" v-model="dueLocal" type="datetime-local" />
          <div class="flex flex-wrap gap-1.5">
            <Button
              v-for="preset in presets"
              :key="preset.key"
              type="button"
              variant="outline"
              size="sm"
              class="h-7 text-xs"
              @click="dueLocal = toLocalInput(preset.date())"
            >{{ $t(`tasks.preset.${preset.key}`) }}</Button>
            <Button type="button" variant="ghost" size="sm" class="h-7 text-xs" @click="dueLocal = ''">
              {{ $t('tasks.noDueDate') }}
            </Button>
          </div>
        </div>

        <div v-if="canPickAssignee" class="space-y-1.5">
          <Label for="followup-assignee">{{ $t('tasks.assignee') }}</Label>
          <Select v-model="assignee">
            <SelectTrigger id="followup-assignee">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem :value="ME">{{ $t('tasks.assignMe') }}</SelectItem>
              <SelectItem v-for="u in assignableUsers" :key="u.id" :value="u.id">{{ u.full_name || u.email }}</SelectItem>
              <SelectItem
                v-if="assignee !== ME && !assignableUsers.some(u => u.id === assignee)"
                :value="assignee"
              >{{ task?.assignee_name || assignee }}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div class="space-y-1.5">
          <Label for="followup-notes">{{ $t('tasks.notes') }}</Label>
          <Textarea id="followup-notes" v-model="notes" :rows="3" />
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" @click="open = false">{{ $t('common.cancel') }}</Button>
          <Button type="submit" :disabled="isSaving">{{ isEdit ? $t('common.save') : $t('tasks.create') }}</Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
</template>
