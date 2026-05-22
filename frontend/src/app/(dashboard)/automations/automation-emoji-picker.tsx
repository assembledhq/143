"use client";

import { useMemo, useState } from "react";
import { ChevronDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

type EmojiOption = {
  emoji: string;
  label: string;
  keywords: string;
};

type EmojiCategory = {
  name: string;
  keywords: string;
  emojis: EmojiOption[];
};

const RECENT_EMOJI_STORAGE_KEY = "automation-emoji-picker-recents";
const MAX_RECENT_EMOJIS = 24;

const KNOWN_LABELS = new Map<string, string>([
  ["⚙️", "Gear"],
  ["🧹", "Broom"],
  ["🧪", "Test tube"],
  ["🚀", "Rocket"],
  ["🔒", "Lock"],
  ["📦", "Package"],
  ["🔍", "Magnifying glass"],
  ["🛠️", "Tools"],
  ["📈", "Chart"],
  ["🤖", "Robot"],
  ["✨", "Sparkles"],
  ["🔥", "Fire"],
  ["✅", "Check mark"],
  ["🚨", "Siren"],
  ["🧠", "Brain"],
  ["💡", "Light bulb"],
  ["🧰", "Toolbox"],
  ["🧯", "Fire extinguisher"],
  ["🩺", "Stethoscope"],
  ["🧭", "Compass"],
]);

const CATEGORY_DATA = [
  {
    name: "Smileys & People",
    keywords: "faces people emotion hands",
    emojis: "😀 😃 😄 😁 😆 😅 😂 🙂 🙃 😉 😊 😇 🥰 😍 🤩 😘 😗 😚 😙 😋 😛 😜 🤪 😝 🤑 🤗 🤭 🤫 🤔 🫡 🤐 🤨 😐 😑 😶 🫥 😏 😒 🙄 😬 🤥 😌 😔 😪 🤤 😴 😷 🤒 🤕 🤢 🤮 🤧 🥵 🥶 🥴 😵 🤯 🤠 🥳 😎 🤓 🧐 😕 🫤 😟 🙁 ☹️ 😮 😯 😲 😳 🥺 🥹 😦 😧 😨 😰 😥 😢 😭 😱 😖 😣 😞 😓 😩 😫 🥱 😤 😡 😠 🤬 😈 👿 💀 ☠️ 💩 🤡 👻 👽 👾 🤖 😺 😸 😹 😻 😼 😽 🙀 😿 😾 👋 🤚 🖐️ ✋ 🖖 👌 🤌 🤏 ✌️ 🤞 🫰 🤟 🤘 🤙 👈 👉 👆 🖕 👇 ☝️ 👍 👎 ✊ 👊 🤛 🤜 👏 🙌 🫶 👐 🤲 🤝 🙏 💅 🤳 💪 🦾 🦿 🦵 🦶 👂 🦻 👃 🧠 🫀 🫁 🦷 🦴 👀 👁️ 👅 👄 🫦",
  },
  {
    name: "Animals & Nature",
    keywords: "animals nature plants weather",
    emojis: "🐶 🐱 🐭 🐹 🐰 🦊 🐻 🐼 🐻‍❄️ 🐨 🐯 🦁 🐮 🐷 🐽 🐸 🐵 🙈 🙉 🙊 🐒 🐔 🐧 🐦 🐤 🐣 🐥 🦆 🦅 🦉 🦇 🐺 🐗 🐴 🦄 🫎 🐝 🪱 🐛 🦋 🐌 🐞 🐜 🪰 🪲 🪳 🦟 🦗 🕷️ 🕸️ 🦂 🐢 🐍 🦎 🦖 🦕 🐙 🦑 🦐 🦞 🦀 🪼 🪸 🐡 🐠 🐟 🐬 🐳 🐋 🦈 🐊 🐅 🐆 🦓 🦍 🦧 🦣 🐘 🦛 🦏 🐪 🐫 🦒 🦘 🦬 🐃 🐂 🐄 🫏 🐎 🐖 🐏 🐑 🦙 🐐 🦌 🐕 🐩 🦮 🐕‍🦺 🐈 🐈‍⬛ 🪶 🪽 🐓 🦃 🦤 🦚 🦜 🦢 🦩 🕊️ 🐇 🦝 🦨 🦡 🦫 🦦 🦥 🐁 🐀 🐿️ 🦔 🌵 🎄 🌲 🌳 🌴 🪵 🌱 🌿 ☘️ 🍀 🎍 🪴 🎋 🍃 🍂 🍁 🪺 🪹 🍄 🐚 🪨 🌾 💐 🌷 🌹 🥀 🪻 🪷 🌺 🌸 🌼 🌻 🌞 🌝 🌛 🌜 🌚 🌕 🌖 🌗 🌘 🌑 🌒 🌓 🌔 🌙 🌎 🌍 🌏 🪐 💫 ⭐ 🌟 ✨ ⚡ ☄️ 💥 🔥 🌪️ 🌈 ☀️ 🌤️ ⛅ 🌥️ ☁️ 🌦️ 🌧️ ⛈️ 🌩️ 🌨️ ❄️ ☃️ ⛄ 🌬️ 💨 💧 💦 ☔ ☂️ 🌊 🌫️",
  },
  {
    name: "Food & Drink",
    keywords: "food drink meals",
    emojis: "🍏 🍎 🍐 🍊 🍋 🍌 🍉 🍇 🍓 🫐 🍈 🍒 🍑 🥭 🍍 🥥 🥝 🍅 🫒 🥑 🍆 🥔 🥕 🌽 🌶️ 🫑 🥒 🥬 🥦 🧄 🧅 🥜 🫘 🌰 🫚 🫛 🍞 🥐 🥖 🫓 🥨 🥯 🥞 🧇 🧀 🍖 🍗 🥩 🥓 🍔 🍟 🍕 🌭 🥪 🌮 🌯 🫔 🥙 🧆 🥚 🍳 🥘 🍲 🫕 🥣 🥗 🍿 🧈 🧂 🥫 🍱 🍘 🍙 🍚 🍛 🍜 🍝 🍠 🍢 🍣 🍤 🍥 🥮 🍡 🥟 🥠 🥡 🦪 🍦 🍧 🍨 🍩 🍪 🎂 🍰 🧁 🥧 🍫 🍬 🍭 🍮 🍯 🍼 🥛 ☕ 🫖 🍵 🍶 🍾 🍷 🍸 🍹 🍺 🍻 🥂 🥃 🫗 🥤 🧋 🧃 🧉 🧊 🥢 🍽️ 🍴 🥄 🔪 🫙 🏺",
  },
  {
    name: "Activity",
    keywords: "activity sports games celebration",
    emojis: "🎃 🎄 🎆 🎇 🧨 ✨ 🎈 🎉 🎊 🎋 🎍 🎎 🎏 🎐 🎑 🧧 🎀 🎁 🎗️ 🎟️ 🎫 🎖️ 🏆 🏅 🥇 🥈 🥉 ⚽ ⚾ 🥎 🏀 🏐 🏈 🏉 🎾 🥏 🎳 🏏 🏑 🏒 🥍 🏓 🏸 🥊 🥋 🥅 ⛳ ⛸️ 🎣 🤿 🎽 🎿 🛷 🥌 🎯 🪀 🪁 🔫 🎱 🔮 🪄 🎮 🕹️ 🎰 🎲 🧩 🧸 🪅 🪩 🪆 ♠️ ♥️ ♦️ ♣️ ♟️ 🃏 🀄 🎴 🎭 🖼️ 🎨 🧵 🪡 🧶 🪢",
  },
  {
    name: "Travel & Places",
    keywords: "travel places transport buildings",
    emojis: "🚗 🚕 🚙 🚌 🚎 🏎️ 🚓 🚑 🚒 🚐 🛻 🚚 🚛 🚜 🦯 🦽 🦼 🛴 🚲 🛵 🏍️ 🛺 🚨 🚔 🚍 🚘 🚖 🚡 🚠 🚟 🚃 🚋 🚞 🚝 🚄 🚅 🚈 🚂 🚆 🚇 🚊 🚉 ✈️ 🛫 🛬 🛩️ 💺 🛰️ 🚀 🛸 🚁 🛶 ⛵ 🚤 🛥️ 🛳️ ⛴️ 🚢 ⚓ 🛟 🪝 ⛽ 🚧 🚦 🚥 🚏 🗺️ 🗿 🗽 🗼 🏰 🏯 🏟️ 🎡 🎢 🎠 ⛲ ⛱️ 🏖️ 🏝️ 🏜️ 🌋 ⛰️ 🏔️ 🗻 🏕️ ⛺ 🛖 🏠 🏡 🏘️ 🏚️ 🏗️ 🏭 🏢 🏬 🏣 🏤 🏥 🏦 🏨 🏪 🏫 🏩 💒 🏛️ ⛪ 🕌 🛕 🕍 🕋 ⛩️ 🛤️ 🛣️ 🗾 🎑 🏞️ 🌅 🌄 🌠 🎇 🎆 🌇 🌆 🏙️ 🌃 🌌 🌉 🌁",
  },
  {
    name: "Objects",
    keywords: "objects tools office automation work",
    emojis: "⌚ 📱 📲 💻 ⌨️ 🖥️ 🖨️ 🖱️ 🖲️ 🕹️ 🗜️ 💽 💾 💿 📀 📼 📷 📸 📹 🎥 📽️ 🎞️ 📞 ☎️ 📟 📠 📺 📻 🎙️ 🎚️ 🎛️ 🧭 ⏱️ ⏲️ ⏰ 🕰️ ⌛ ⏳ 📡 🔋 🪫 🔌 💡 🔦 🕯️ 🪔 🧯 🛢️ 💸 💵 💴 💶 💷 🪙 💰 💳 🧾 💎 ⚖️ 🪜 🧰 🪛 🔧 🔨 ⚒️ 🛠️ ⛏️ 🪚 🔩 ⚙️ 🪤 🧱 ⛓️ 🧲 🔫 💣 🧨 🪓 🔪 🗡️ ⚔️ 🛡️ 🚬 ⚰️ 🪦 ⚱️ 🏺 🔮 📿 🧿 🪬 💈 ⚗️ 🔭 🔬 🕳️ 🩹 🩺 💊 💉 🩸 🧬 🦠 🧫 🧪 🌡️ 🧹 🪠 🧺 🧻 🚽 🚰 🚿 🛁 🛀 🧼 🪥 🪒 🧽 🪣 🧴 🛎️ 🔑 🗝️ 🚪 🪑 🛋️ 🛏️ 🛌 🧸 🪆 🖼️ 🪞 🪟 🛍️ 🛒 🎁 🎈 🎏 🎀 🪄 🪅 🪩 🎊 🎉 📨 📩 📤 📥 📦 🏷️ 🪧 📪 📫 📬 📭 📮 📯 📜 📃 📄 📑 🧾 📊 📈 📉 🗒️ 🗓️ 📆 📅 🗑️ 🪪 📇 🗃️ 🗳️ 🗄️ 📋 📁 📂 🗂️ 🗞️ 📰 📓 📔 📒 📕 📗 📘 📙 📚 📖 🔖 🧷 🔗 📎 🖇️ 📐 📏 🧮 📌 📍 ✂️ 🖊️ 🖋️ ✒️ 🖌️ 🖍️ 📝 ✏️ 🔍 🔎 🔏 🔐 🔒 🔓",
  },
  {
    name: "Symbols",
    keywords: "symbols signs marks arrows",
    emojis: "❤️ 🧡 💛 💚 💙 💜 🖤 🤍 🤎 💔 ❤️‍🔥 ❤️‍🩹 ❣️ 💕 💞 💓 💗 💖 💘 💝 💟 ☮️ ✝️ ☪️ 🕉️ ☸️ ✡️ 🔯 🕎 ☯️ ☦️ 🛐 ⛎ ♈ ♉ ♊ ♋ ♌ ♍ ♎ ♏ ♐ ♑ ♒ ♓ 🆔 ⚛️ 🉑 ☢️ ☣️ 📴 📳 🈶 🈚 🈸 🈺 🈷️ ✴️ 🆚 💮 🉐 ㊙️ ㊗️ 🈴 🈵 🈹 🈲 🅰️ 🅱️ 🆎 🆑 🅾️ 🆘 ❌ ⭕ 🛑 ⛔ 📛 🚫 💯 💢 ♨️ 🚷 🚯 🚳 🚱 🔞 📵 🚭 ❗ ❕ ❓ ❔ ‼️ ⁉️ 🔅 🔆 〽️ ⚠️ 🚸 🔱 ⚜️ 🔰 ♻️ ✅ 🈯 💹 ❇️ ✳️ ❎ 🌐 💠 Ⓜ️ 🌀 💤 🏧 🚾 ♿ 🅿️ 🛗 🈳 🈂️ 🛂 🛃 🛄 🛅 🚹 🚺 🚼 ⚧️ 🚻 🚮 🎦 📶 🈁 🔣 ℹ️ 🔤 🔡 🔠 🆖 🆗 🆙 🆒 🆕 🆓 0️⃣ 1️⃣ 2️⃣ 3️⃣ 4️⃣ 5️⃣ 6️⃣ 7️⃣ 8️⃣ 9️⃣ 🔟 🔢 ▶️ ⏸️ ⏯️ ⏹️ ⏺️ ⏭️ ⏮️ ⏩ ⏪ ⏫ ⏬ ◀️ 🔼 🔽 ➡️ ⬅️ ⬆️ ⬇️ ↗️ ↘️ ↙️ ↖️ ↕️ ↔️ ↪️ ↩️ ⤴️ ⤵️ 🔀 🔁 🔂 🔄 🔃 🎵 🎶 ➕ ➖ ➗ ✖️ 🟰 ♾️ 💲 💱 ™️ ©️ ®️ 〰️ ➰ ➿ 🔚 🔙 🔛 🔝 🔜 ✔️ ☑️ 🔘 🔴 🟠 🟡 🟢 🔵 🟣 ⚫ ⚪ 🟤 🔺 🔻 🔸 🔹 🔶 🔷 🔳 🔲 ▪️ ▫️ ◾ ◽ ◼️ ◻️ 🟥 🟧 🟨 🟩 🟦 🟪 ⬛ ⬜ 🟫",
  },
  {
    name: "Flags",
    keywords: "flags countries",
    emojis: "🏁 🚩 🎌 🏴 🏳️ 🏳️‍🌈 🏳️‍⚧️ 🏴‍☠️ 🇺🇸 🇨🇦 🇲🇽 🇧🇷 🇦🇷 🇨🇱 🇨🇴 🇵🇪 🇬🇧 🇮🇪 🇫🇷 🇩🇪 🇮🇹 🇪🇸 🇵🇹 🇳🇱 🇧🇪 🇨🇭 🇦🇹 🇩🇰 🇸🇪 🇳🇴 🇫🇮 🇵🇱 🇨🇿 🇬🇷 🇹🇷 🇺🇦 🇯🇵 🇰🇷 🇨🇳 🇮🇳 🇸🇬 🇦🇺 🇳🇿 🇿🇦 🇪🇬 🇳🇬 🇰🇪 🇮🇱 🇦🇪 🇸🇦",
  },
] as const;

const makeOption = (emoji: string, category: string, categoryKeywords: string): EmojiOption => {
  const label = KNOWN_LABELS.get(emoji) ?? `${category} emoji ${emoji}`;
  return {
    emoji,
    label,
    keywords: `${label} ${emoji} ${category} ${categoryKeywords}`,
  };
};

const EMOJI_CATEGORIES: EmojiCategory[] = CATEGORY_DATA.map((category) => ({
  name: category.name,
  keywords: category.keywords,
  emojis: category.emojis
    .split(/\s+/)
    .filter(Boolean)
    .filter((emoji, index, emojis) => emojis.indexOf(emoji) === index)
    .map((emoji) => makeOption(emoji, category.name, category.keywords)),
}));

const AUTOMATION_EMOJIS: EmojiOption[] = (() => {
  const seen = new Set<string>();
  return EMOJI_CATEGORIES.flatMap((category) => category.emojis).filter((item) => {
    if (seen.has(item.emoji)) return false;
    seen.add(item.emoji);
    return true;
  });
})();

const AUTOMATION_EMOJI_BY_VALUE = new Map(AUTOMATION_EMOJIS.map((item) => [item.emoji, item]));

const readRecentEmojis = () => {
  if (typeof window === "undefined") return [];

  try {
    const parsed = JSON.parse(window.localStorage.getItem(RECENT_EMOJI_STORAGE_KEY) ?? "[]");
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((item): item is string => typeof item === "string").slice(0, MAX_RECENT_EMOJIS);
  } catch {
    return [];
  }
};

const writeRecentEmojis = (emojis: string[]) => {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(RECENT_EMOJI_STORAGE_KEY, JSON.stringify(emojis.slice(0, MAX_RECENT_EMOJIS)));
  } catch (error) {
    console.error("Failed to persist recent emoji selection", error);
  }
};

export function AutomationEmojiPicker({
  value,
  onChange,
  className,
  open,
  onOpenChange,
  trigger = "select",
  triggerLabel = "Automation emoji",
  disabled = false,
}: {
  value: string;
  onChange: (value: string) => void;
  className?: string;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  trigger?: "select" | "icon";
  triggerLabel?: string;
  disabled?: boolean;
}) {
  const [internalOpen, setInternalOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [recentEmojis, setRecentEmojis] = useState<string[]>(readRecentEmojis);
  const pickerOpen = open ?? internalOpen;
  const setPickerOpen = onOpenChange ?? setInternalOpen;

  const selected = useMemo(
    () => AUTOMATION_EMOJI_BY_VALUE.get(value) ?? { emoji: value || "⚙️", label: "Selected emoji", keywords: value || "gear" },
    [value],
  );
  const recentOptions = useMemo(
    () => recentEmojis.map((emoji) => AUTOMATION_EMOJI_BY_VALUE.get(emoji) ?? makeOption(emoji, "Frequently Used", "recent")).filter((item) => item.emoji),
    [recentEmojis],
  );
  const visibleGroups = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    if (!normalizedQuery) {
      return [
        ...(recentOptions.length > 0 ? [{ name: "Frequently Used", emojis: recentOptions }] : []),
        ...EMOJI_CATEGORIES.map((category) => ({
          name: category.name,
          emojis: category.emojis,
        })),
      ];
    }

    return [{
      name: "Search Results",
      emojis: AUTOMATION_EMOJIS.filter((item) => item.keywords.toLowerCase().includes(normalizedQuery)),
    }];
  }, [query, recentOptions]);

  const selectEmoji = (emoji: string) => {
    const nextRecentEmojis = [emoji, ...recentEmojis.filter((item) => item !== emoji)].slice(0, MAX_RECENT_EMOJIS);
    setRecentEmojis(nextRecentEmojis);
    writeRecentEmojis(nextRecentEmojis);
    onChange(emoji);
    setPickerOpen(false);
  };

  return (
    <Popover open={pickerOpen} onOpenChange={setPickerOpen}>
      <PopoverTrigger asChild>
        {trigger === "icon" ? (
          <Button
            type="button"
            variant="outline"
            size="icon-lg"
            aria-label={triggerLabel}
            disabled={disabled}
            className={cn("text-lg leading-none", className)}
          >
            {selected.emoji}
          </Button>
        ) : (
          <Button
            type="button"
            variant="outline"
            aria-label={triggerLabel}
            disabled={disabled}
            className={cn("h-10 w-16 justify-center gap-1 px-2", className)}
          >
            <span className="text-lg leading-none" aria-hidden="true">{selected.emoji}</span>
            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          </Button>
        )}
      </PopoverTrigger>
      <PopoverContent className="w-[22rem] p-0" align="start">
        <Command shouldFilter={false}>
          <CommandInput
            placeholder="Search emoji..."
            value={query}
            onValueChange={setQuery}
          />
          <CommandList className="max-h-[22rem] px-2 py-2">
            <CommandEmpty>No emoji found.</CommandEmpty>
            {visibleGroups.map((group) => (
              <CommandGroup
                key={group.name}
                heading={group.name}
                className="[&_[cmdk-group-heading]]:sticky [&_[cmdk-group-heading]]:top-0 [&_[cmdk-group-heading]]:z-10 [&_[cmdk-group-heading]]:bg-popover [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-wide [&_[cmdk-group-items]]:grid [&_[cmdk-group-items]]:grid-cols-8 [&_[cmdk-group-items]]:gap-1"
              >
                {group.emojis.map((item, index) => (
                  <CommandItem
                    key={`${group.name}-${item.emoji}-${index}`}
                    value={item.keywords}
                    aria-label={item.label}
                    className={cn(
                      "flex h-9 w-9 cursor-pointer items-center justify-center rounded-md p-0 text-lg leading-none transition-colors hover:bg-accent",
                      item.emoji === selected.emoji && "bg-primary text-primary-foreground data-[selected=true]:bg-primary data-[selected=true]:text-primary-foreground",
                    )}
                    onSelect={() => selectEmoji(item.emoji)}
                  >
                    <span aria-hidden="true">{item.emoji}</span>
                    <span className="sr-only">{item.label}</span>
                  </CommandItem>
                ))}
              </CommandGroup>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
