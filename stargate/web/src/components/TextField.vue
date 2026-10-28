<script setup lang="ts">
import { computed, ref, useAttrs, useId } from 'vue'

// Every attribute belongs to the control, never to the layout box around it:
// an `aria-label` on a `<div>` describes nothing. The root still needs
// `inheritAttrs: false` or Vue would write them there as well.
defineOptions({ inheritAttrs: false })

/**
 * The console's single field: a Material-style filled control whose label
 * animates between two positions.
 *
 * At rest the label sits on the value line, so an empty form reads as a list of
 * questions instead of a row of blank boxes. On focus, or once there is a
 * value, it rises into a caption above the value and stays there. The
 * indicator along the bottom edge grows from the centre while focused, and is
 * painted permanently in the error tone when `invalid` is set, so a failed
 * field is visible without the pointer being in it.
 *
 * A field whose name lives in the surrounding chrome — a search box in a
 * toolbar, a filter in a table header — sets `bare`: no caption, no reserved
 * caption line, and its name comes from `aria-label` through attribute
 * fallthrough.
 *
 * The component renders the control itself rather than wrapping a slot, so
 * `v-model`, `id`, `type`, `inputmode`, `autocomplete`, `rows`, `@keydown`, and
 * the rest stay on a real `<input>`/`<textarea>`/`<select>` and behave exactly
 * as written. Attributes land on the control, not on the wrapper, because the
 * wrapper is a layout box and an `aria-*` on it would describe nothing.
 */
const props = withDefaults(
  defineProps<{
    /** Visible caption. Required unless `bare`. */
    label?: string
    /** Which element to render. */
    as?: 'input' | 'textarea' | 'select'
    /** Height tier; matches the button size it sits beside. */
    size?: 'md' | 'sm' | 'xs'
    /** Input type; ignored by textarea and select. */
    type?: string
    /** Drops the caption and its reserved line, for chrome-level controls. */
    bare?: boolean
    /** Draws the field in the error tone and marks the control invalid. */
    invalid?: boolean
    /** Helper text under the field, in the error tone when `invalid`. */
    hint?: string
    /** Tints the hint without marking the control invalid. */
    hintTone?: 'warning'
    placeholder?: string
    disabled?: boolean
    /** Visible lines for a textarea; ignored by the other elements. */
    rows?: number
    /** Extra classes for the control itself, e.g. `font-mono`. */
    controlClass?: string
    /** Extra classes for the wrapper, for spacing at the call site. */
    rootClass?: string
    /** Vue's `.number` modifier on a component `v-model`. */
    modelModifiers?: { number?: boolean }
  }>(),
  { as: 'input', size: 'sm', type: 'text' },
)

const model = defineModel<string | number>({ default: '' })

const attrs = useAttrs()

const generatedId = useId()
/**
 * The caller's `id` wins, so an existing `<label for>` in a view keeps
 * pointing at the control; otherwise the field generates one and its own
 * caption points at that.
 */
const controlId = computed(() =>
  typeof attrs.id === 'string' && attrs.id !== '' ? attrs.id : generatedId,
)

const control = ref<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement | null>(null)

/** Whether the control has a value the caption must clear. */
const filled = computed(() => {
  // A select's first option is a real choice, and a textarea has no single
  // value line for a caption to rest on, so both always show the caption.
  if (props.as === 'select' || props.as === 'textarea') return true
  const value = model.value
  return value !== '' && value !== null && value !== undefined
})

/**
 * A caption exists only when the field has one and is not `bare`. Everything
 * that reserves space for the caption keys off this, so a label-less row keeps
 * its natural height instead of carrying an empty line above the value.
 */
const hasLabel = computed(() => Boolean(props.label) && !props.bare)

const floated = computed(() => filled.value || !hasLabel.value)

/**
 * Writes the control's value back through `v-model`. A select with `.number`
 * (the status and contact-type pickers) needs the coercion Vue would have
 * applied itself, because the inner element is bound by hand rather than with
 * `v-model`.
 */
function commit(event: Event) {
  const target = event.target as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement
  const raw = target.value
  model.value = props.modelModifiers?.number ? Number(raw) : raw
}

defineExpose({
  focus: () => control.value?.focus(),
  el: control,
})
</script>

<template>
  <div
    :class="[
      'field',
      `field-${props.size}`,
      props.rootClass,
      { 'field-bare': !hasLabel, 'is-floated': floated, 'is-invalid': props.invalid },
    ]"
  >
    <div class="field-box">
      <label v-if="hasLabel" :for="controlId" class="field-label-float">
        {{ props.label }}
      </label>

      <textarea
        v-if="props.as === 'textarea'"
        :id="controlId"
        ref="control"
        v-bind="$attrs"
        :rows="props.rows ?? 3"
        :class="['field-control', props.controlClass]"
        :value="model"
        :placeholder="props.placeholder"
        :disabled="props.disabled"
        :aria-invalid="props.invalid || undefined"
        @input="commit"
      />

      <select
        v-else-if="props.as === 'select'"
        :id="controlId"
        ref="control"
        v-bind="$attrs"
        :class="['field-control', props.controlClass]"
        :value="model"
        :disabled="props.disabled"
        :aria-invalid="props.invalid || undefined"
        @change="commit"
      >
        <slot />
      </select>

      <input
        v-else
        :id="controlId"
        ref="control"
        v-bind="$attrs"
        :class="['field-control', props.controlClass]"
        :type="props.type"
        :value="model"
        :placeholder="props.placeholder"
        :disabled="props.disabled"
        :aria-invalid="props.invalid || undefined"
        @input="commit"
      />

      <slot name="action" />
    </div>

    <p
      v-if="props.hint"
      class="field-hint"
      :class="{
        'field-hint-error': props.invalid,
        'field-hint-warning': !props.invalid && props.hintTone === 'warning',
      }"
    >
      {{ props.hint }}
    </p>
  </div>
</template>
