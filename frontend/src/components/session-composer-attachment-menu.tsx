"use client";

import { Link as LinkIcon, Loader2, Paperclip, Plus } from "lucide-react";
import { LinearIcon } from "@/components/linear-icon";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

type SessionComposerAttachmentMenuProps = {
  disabled?: boolean;
  isUploading?: boolean;
  onUploadFiles: () => void;
  onAddImageURL: () => void;
  onAddLinearIssue: () => void;
  showLinearIssue?: boolean;
  buttonAriaLabel: string;
  buttonTitle?: string;
  buttonClassName?: string;
};

export function SessionComposerAttachmentMenu({
  disabled = false,
  isUploading = false,
  onUploadFiles,
  onAddImageURL,
  onAddLinearIssue,
  showLinearIssue = true,
  buttonAriaLabel,
  buttonTitle,
  buttonClassName,
}: SessionComposerAttachmentMenuProps) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className={buttonClassName}
          title={buttonTitle}
          aria-label={buttonAriaLabel}
          disabled={disabled}
        >
          {isUploading ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        <DropdownMenuItem disabled={isUploading} onClick={onUploadFiles}>
          <Paperclip className="mr-2 h-4 w-4" />
          Upload files or photos
        </DropdownMenuItem>
        <DropdownMenuItem onClick={onAddImageURL}>
          <LinkIcon data-testid="add-image-url-link-icon" className="mr-2 h-4 w-4" />
          Add image URL
        </DropdownMenuItem>
        {showLinearIssue ? (
          <DropdownMenuItem onClick={onAddLinearIssue}>
            <LinearIcon className="mr-2 h-4 w-4" />
            Add linear issue
          </DropdownMenuItem>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
