import { getImage } from "astro:assets";
import socialCard from "../../assets/images/og/arkoren-og.png";

/** Keep the social card a full-resolution PNG for sharing-platform support. */
export const socialImage = await getImage({
  src: socialCard,
  width: 1200,
  height: 630,
  format: "png",
});
