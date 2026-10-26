<script lang="ts">
  export let name: "plus" | "close" | "arrow-up" | "arrow-down" | "arrow-up-right" | "search" | "refresh" | "board" | "list" | "clock";
</script>

<svg class="icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false"><use href={`${import.meta.env.BASE_URL}icons.svg#${name}`}></use></svg>
